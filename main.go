package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/orzogc/acfundanmu"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fastjson"
	_ "modernc.org/sqlite"
)

const createTable = `CREATE TABLE IF NOT EXISTS acfunlive (
	liveID TEXT PRIMARY KEY,
	uid INTEGER NOT NULL,
	name TEXT NOT NULL,
	streamName TEXT NOT NULL UNIQUE,
	startTime INTEGER NOT NULL,
	title TEXT NOT NULL,
	duration INTEGER NOT NULL,
	playbackURL TEXT NOT NULL,
	backupURL TEXT NOT NULL,
	aliURL TEXT NOT NULL,
	txURL TEXT NOT NULL
);
`

const insertLive = `INSERT OR IGNORE INTO acfunlive
	(liveID, uid, name, streamName, startTime, title, duration, playbackURL, backupURL, aliURL, txURL)
	VALUES
	(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
`

const updatePlayback = `UPDATE acfunlive SET
	duration = ?,
	playbackURL = ?,
	backupURL = ?,
	aliURL = ?,
	txURL = ?
	WHERE liveID = ?;
`

const selectUID = `SELECT * FROM acfunlive
	WHERE uid = ?
	ORDER BY startTime DESC
	LIMIT ?;
`

type live struct {
	liveID      string // 直播ID
	uid         int    // 主播uid
	name        string // 主播昵称
	streamName  string // 直播源ID
	startTime   int64  // 直播开始时间，单位为毫秒
	title       string // 直播间标题
	duration    int64  // 录播时长，单位为毫秒
	playbackURL string // 录播链接
	backupURL   string // 录播备份链接
	aliURL      string // 阿里云录播源链接，目前阿里云的下载速度比较快
	txURL       string // 腾讯云录播源链接
}

var client = &fasthttp.Client{
	MaxIdleConnDuration: 90 * time.Second,
	ReadTimeout:         10 * time.Second,
	WriteTimeout:        10 * time.Second,
}

var didCookie *fasthttp.Cookie

var parserPool fastjson.ParserPool

var quit = make(chan int)

var dq *acfundanmu.DanmuQueue

// 检查错误
func checkErr(err error) {
	if err != nil {
		panic(err)
	}
}

// 获取正在直播的直播间列表数据
func fetchLiveList() (list map[string]live, e error) {
	defer func() {
		if err := recover(); err != nil {
			e = fmt.Errorf("fetchLiveRoom() error: %w", err)
		}
	}()

	const liveListURL = "https://live.acfun.cn/api/channel/list?count=%d"

	p := parserPool.Get()
	defer parserPool.Put(p)
	var v *fastjson.Value
	for count := 1000; count < 1e8; count *= 10 {
		req := fasthttp.AcquireRequest()
		defer fasthttp.ReleaseRequest(req)
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseResponse(resp)
		req.SetRequestURI(fmt.Sprintf(liveListURL, count))
		req.Header.SetMethod("GET")
		err := client.Do(req, resp)
		checkErr(err)
		body := resp.Body()

		v, err = p.ParseBytes(body)
		checkErr(err)
		v = v.Get("channelListData")
		if !v.Exists("result") || v.GetInt("result") != 0 {
			panic(fmt.Errorf("获取正在直播的直播间列表失败"))
		}
		cursor := string(v.GetStringBytes("pcursor"))
		if cursor == "no_more" {
			break
		}
	}

	list = make(map[string]live)
	liveList := v.GetArray("liveList")
	for _, liveRoom := range liveList {
		l := live{
			liveID:     string(liveRoom.GetStringBytes("liveId")),
			uid:        liveRoom.GetInt("authorId"),
			name:       string(liveRoom.GetStringBytes("user", "name")),
			streamName: string(liveRoom.GetStringBytes("streamName")),
			startTime:  liveRoom.GetInt64("createTime"),
			title:      string(liveRoom.GetStringBytes("title")),
		}
		list[l.liveID] = l
	}

	return list, nil
}

// 处理退出信号
func quitSignal(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, os.Kill, syscall.SIGTERM)

	select {
	case <-ch:
	case <-quit:
	}

	log.Println("正在退出本程序，请等待")
	cancel()
}

// stime以毫秒为单位，返回具体开播时间
func startTime(stime int64) string {
	t := time.Unix(stime/1e3, 0)
	return fmt.Sprintf("%d-%02d-%02d %02d:%02d:%02d", t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second())
}

// dtime以毫秒为单位，返回具体直播时长
func duration(dtime int64) string {
	t := time.Unix(dtime/1e3, 0).UTC()
	return fmt.Sprintf("%02d:%02d:%02d", t.Hour(), t.Minute(), t.Second())
}

// 处理查询
func handleQuery(ctx context.Context, stmt *sql.Stmt, uid int, count int) {
	l := live{}
	rows, err := stmt.QueryContext(ctx, uid, count)
	checkErr(err)
	defer rows.Close()
	for rows.Next() {
		err = rows.Scan(&l.liveID, &l.uid, &l.name, &l.streamName, &l.startTime, &l.title, &l.duration, &l.playbackURL, &l.backupURL, &l.aliURL, &l.txURL)
		checkErr(err)
		fmt.Printf("开播时间：%s 主播uid：%d 昵称：%s 直播标题：%s liveID: %s streamName: %s 直播时长：%s 录播链接：%s 录播备份链接：%s 录播阿里云链接：%s 录播腾讯云链接: %s\n",
			startTime(l.startTime), l.uid, l.name, l.title, l.liveID, l.streamName, duration(l.duration), l.playbackURL, l.backupURL, l.aliURL, l.txURL,
		)
	}
	err = rows.Err()
	if errors.Is(err, sql.ErrNoRows) {
		log.Printf("没有uid为 %d 的主播的记录", uid)
	} else {
		checkErr(err)
	}
}

// 处理输入
func handleInput(ctx context.Context, db *sql.DB) {
	const helpMsg = `请输入"listall 主播的uid"、"list20 主播的uid"、"getplayback liveID"或"quit"`

	selectStmt, err := db.PrepareContext(ctx, selectUID)
	checkErr(err)
	defer selectStmt.Close()

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		cmd := strings.Fields(scanner.Text())
		if len(cmd) == 1 && cmd[0] == "quit" {
			quit <- 0
			break
		}
		if len(cmd) != 2 {
			log.Println(helpMsg)
			continue
		}
		if uid, err := strconv.ParseUint(cmd[1], 10, 64); err != nil {
			if cmd[0] == "getplayback" {
				playback, err := getPlayback(cmd[1])
				if err != nil {
					log.Println(err)
				} else {
					log.Printf("liveID %s 的查询结果是：\n录播链接：%s\n录播备份链接：%s\n录播阿里云链接：%s\n录播腾讯云链接: %s",
						cmd[1], playback.URL, playback.BackupURL, playback.AliURL, playback.TxURL,
					)
				}
			} else {
				log.Println(helpMsg)
			}
		} else {
			switch cmd[0] {
			case "listall":
				handleQuery(ctx, selectStmt, int(uid), -1)
			case "list20":
				handleQuery(ctx, selectStmt, int(uid), 20)
			default:
				log.Println(helpMsg)
			}
		}
	}
	err = scanner.Err()
	checkErr(err)
}

// 获取指定liveID的playback
func getPlayback(liveID string) (playback acfundanmu.Playback, err error) {
	for retry := 0; retry < 3; retry++ {
		playback, err = dq.GetPlayback(liveID)
		if err != nil {
			log.Printf("获取liveID为 %s 的playback出现错误：%+v", liveID, err)
			if retry == 2 {
				return acfundanmu.Playback{}, fmt.Errorf("获取liveID为 %s 的playback失败：%w", liveID, err)
			}
			log.Println("尝试重新获取playback")
		} else {
			break
		}
		time.Sleep(10 * time.Second)
	}

	return playback, nil
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go quitSignal(cancel)

	path, err := os.Executable()
	checkErr(err)
	dir := filepath.Dir(path)
	dbFile := filepath.Join(dir, "acfunlive.db")

	db, err := sql.Open("sqlite", dbFile)
	checkErr(err)
	defer db.Close()
	err = db.Ping()
	checkErr(err)
	_, err = db.ExecContext(ctx, createTable)
	checkErr(err)

	insertStmt, err := db.PrepareContext(ctx, insertLive)
	checkErr(err)
	defer insertStmt.Close()
	updateStmt, err := db.PrepareContext(ctx, updatePlayback)
	checkErr(err)
	defer updateStmt.Close()

	dq, err = acfundanmu.Init(0)
	checkErr(err)
	go handleInput(childCtx, db)

	oldList := make(map[string]live)
Loop:
	for {
		select {
		case <-childCtx.Done():
			break Loop
		default:
			var newList map[string]live
			for retry := 0; retry < 3; retry++ {
				newList, err = fetchLiveList()
				if err != nil {
					log.Printf("获取直播间列表数据出现错误：%v", err)
					if retry == 2 {
						log.Println("获取直播间列表数据出现过多错误，退出本程序")
						panic("获取直播间列表数据出现过多错误")
					}
					log.Println("尝试重新获取直播间列表数据")
				} else {
					break
				}
				time.Sleep(10 * time.Second)
			}

			if len(newList) == 0 {
				log.Println("没有人在直播")
			}

			for _, l := range newList {
				if _, ok := oldList[l.liveID]; !ok {
					// 新的liveID
					_, err = insertStmt.ExecContext(ctx,
						l.liveID, l.uid, l.name, l.streamName, l.startTime, l.title, l.duration, l.playbackURL, l.backupURL, l.aliURL, l.txURL,
					)
					checkErr(err)
				}
			}

			for _, l := range oldList {
				if _, ok := newList[l.liveID]; !ok {
					// liveID对应的直播结束
					go func(l live) {
						time.Sleep(10 * time.Second)
						playback, err := getPlayback(l.liveID)
						if err != nil {
							log.Printf("获取 %s（%d） 的liveID为 %s 的playback出现错误：%+v", l.name, l.uid, l.liveID, err)
							return
						}
						if playback.URL == "" {
							log.Printf("录播链接为空，无法获取 %s（%d） 的liveID为 %s 的主播的录播链接", l.name, l.uid, l.liveID)
							return
						}
						_, err = insertStmt.ExecContext(ctx,
							l.liveID, l.uid, l.name, l.streamName, l.startTime, l.title, l.duration, l.playbackURL, l.backupURL, l.aliURL, l.txURL,
						)
						checkErr(err)
						_, err = updateStmt.ExecContext(ctx,
							playback.Duration, playback.URL, playback.BackupURL, playback.AliURL, playback.TxURL, l.liveID,
						)
						checkErr(err)
						// 需要获取完整的录播链接
						for i := 0; i < 30; i++ {
							time.Sleep(time.Minute)
							playback, err = getPlayback(l.liveID)
							if err != nil {
								log.Printf("获取 %s（%d） 的liveID为 %s 的playback出现错误：%+v", l.name, l.uid, l.liveID, err)
								return
							}
							if strings.Contains(playback.URL, ".0-0.0.m3u8") {
								_, err = updateStmt.ExecContext(ctx,
									playback.Duration, playback.URL, playback.BackupURL, playback.AliURL, playback.TxURL, l.liveID,
								)
								checkErr(err)
								break
							}
							if i == 29 {
								log.Printf("无法获取 %s（%d） 的liveID为 %s 的完整录播链接", l.name, l.uid, l.liveID)
							}
						}
					}(l)
				}
			}

			oldList = newList
			time.Sleep(20 * time.Second)
		}
	}
}
