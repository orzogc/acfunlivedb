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
	"sync"
	"syscall"
	"time"

	"github.com/orzogc/acfundanmu"
	"github.com/valyala/fasthttp"
	"github.com/valyala/fastjson"
	_ "modernc.org/sqlite"
)

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
}

var client = &fasthttp.Client{
	MaxIdleConnDuration: 90 * time.Second,
	ReadTimeout:         10 * time.Second,
	WriteTimeout:        10 * time.Second,
}

var (
	parserPool fastjson.ParserPool
	quit       = make(chan int)
	ac         *acfundanmu.AcFunLive
)

var livePool = &sync.Pool{
	New: func() interface{} {
		return new(live)
	},
}

// 检查错误
func checkErr(err error) {
	if err != nil {
		panic(err)
	}
}

// 获取正在直播的直播间列表数据
func fetchLiveList() (list map[string]*live, e error) {
	defer func() {
		if err := recover(); err != nil {
			e = fmt.Errorf("fetchLiveRoom() error: %w", err)
		}
	}()

	//const liveListURL = "https://live.acfun.cn/api/channel/list?count=%d"
	const liveListURL = "https://api-new.app.acfun.cn/rest/app/live/channel"

	p := parserPool.Get()
	defer parserPool.Put(p)
	var v *fastjson.Value

	for count := 1000; count < 1e8; count *= 10 {
		req := fasthttp.AcquireRequest()
		defer fasthttp.ReleaseRequest(req)
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseResponse(resp)
		req.SetRequestURI(liveListURL)
		req.Header.SetMethod(fasthttp.MethodPost)
		req.Header.SetContentType("application/x-www-form-urlencoded")
		form := fasthttp.AcquireArgs()
		defer fasthttp.ReleaseArgs(form)
		form.Set("count", strconv.Itoa(count))
		form.Set("pcursor", "0")
		req.SetBody(form.QueryString())
		req.Header.Set("Accept-Encoding", "gzip")
		err := client.Do(req, resp)
		checkErr(err)
		var body []byte
		if string(resp.Header.Peek("content-encoding")) == "gzip" || string(resp.Header.Peek("Content-Encoding")) == "gzip" {
			body, err = resp.BodyGunzip()
			checkErr(err)
		} else {
			body = resp.Body()
		}

		v, err = p.ParseBytes(body)
		checkErr(err)
		if !v.Exists("result") || v.GetInt("result") != 0 {
			panic(fmt.Errorf("获取正在直播的直播间列表失败，响应为 %s", string(body)))
		}
		if string(v.GetStringBytes("pcursor")) == "no_more" {
			break
		}
		if count == 1e7 {
			panic(fmt.Errorf("获取正在直播的直播间列表失败"))
		}
	}

	liveList := v.GetArray("liveList")
	list = make(map[string]*live, len(liveList))
	for _, liveRoom := range liveList {
		l := livePool.Get().(*live)
		l.liveID = string(liveRoom.GetStringBytes("liveId"))
		l.uid = liveRoom.GetInt("authorId")
		l.name = string(liveRoom.GetStringBytes("user", "name"))
		l.streamName = string(liveRoom.GetStringBytes("streamName"))
		l.startTime = liveRoom.GetInt64("createTime")
		l.title = string(liveRoom.GetStringBytes("title"))
		l.duration = 0
		l.playbackURL = ""
		l.backupURL = ""
		list[l.liveID] = l
	}

	return list, nil
}

// 处理退出信号
func quitSignal(cancel context.CancelFunc) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	select {
	case <-ch:
	case <-quit:
	}

	signal.Stop(ch)
	signal.Reset(os.Interrupt, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

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
func handleQuery(ctx context.Context, uid, count int) {
	l := live{}
	rows, err := selectUIDStmt.QueryContext(ctx, uid, count)
	checkErr(err)
	defer rows.Close()
	for rows.Next() {
		err = rows.Scan(&l.liveID, &l.uid, &l.name, &l.streamName, &l.startTime, &l.title, &l.duration, &l.playbackURL, &l.backupURL)
		checkErr(err)
		fmt.Printf("开播时间：%s 主播uid：%d 昵称：%s 直播标题：%s liveID: %s streamName: %s 直播时长：%s 录播链接：%s 录播备份链接：%s\n",
			startTime(l.startTime), l.uid, l.name, l.title, l.liveID, l.streamName, duration(l.duration), l.playbackURL, l.backupURL,
		)
	}
	err = rows.Err()
	if errors.Is(err, sql.ErrNoRows) {
		log.Printf("没有uid为 %d 的主播的记录", uid)
	} else {
		checkErr(err)
	}
}

// 处理更新
func handleUpdate(ctx context.Context, uid, count int) {
	l := live{}
	var liveIDList []string
	log.Printf("开始更新uid为 %d 的主播的录播链接，请等待", uid)
	rows, err := selectUIDStmt.QueryContext(ctx, uid, count)
	checkErr(err)
	defer rows.Close()
	for rows.Next() {
		err = rows.Scan(&l.liveID, &l.uid, &l.name, &l.streamName, &l.startTime, &l.title, &l.duration, &l.playbackURL, &l.backupURL)
		checkErr(err)
		liveIDList = append(liveIDList, l.liveID)
	}
	err = rows.Err()
	if errors.Is(err, sql.ErrNoRows) {
		log.Printf("没有uid为 %d 的主播的记录，更新失败", uid)
		return
	}
	checkErr(err)
	for _, liveID := range liveIDList {
		playback, err := getPlayback(liveID)
		if err != nil {
			log.Println(err)
		} else {
			if playback.URL != "" || playback.BackupURL != "" {
				update(ctx, liveID, playback)
			}
		}
	}
	log.Printf("更新uid为 %d 的主播的录播链接成功", uid)
}

// 处理输入
func handleInput(ctx context.Context) {
	const helpMsg = `请输入"listall 主播的uid"、"list10 主播的uid"、"updateall 主播的uid"、"update10 主播的uid"、"getplayback liveID"或"quit"`

	var err error
	selectUIDStmt, err = db.PrepareContext(ctx, selectUID)
	checkErr(err)
	defer selectUIDStmt.Close()

	selectLiveIDStmt, err = db.PrepareContext(ctx, selectLiveID)
	checkErr(err)
	defer selectLiveIDStmt.Close()

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		cmd := strings.Fields(scanner.Text())
		if len(cmd) == 1 {
			if cmd[0] == "quit" {
				quit <- 0
				break
			}
			log.Println(helpMsg)
			continue
		}
		if len(cmd) != 2 {
			log.Println(helpMsg)
			continue
		}
		if uid, err := strconv.ParseUint(cmd[1], 10, 64); err != nil {
			if cmd[0] == "getplayback" {
				liveID := cmd[1]
				log.Printf("查询liveID为 %s 的录播链接，请等待", liveID)
				playback, err := getPlayback(liveID)
				if err != nil {
					log.Println(err)
				} else {
					if playback.URL != "" || playback.BackupURL != "" {
						if queryExist(ctx, liveID) {
							update(ctx, liveID, playback)
						}
					}
					log.Printf("liveID为 %s 的录播查询结果是：\n录播链接：%s\n录播备份链接：%s",
						liveID, playback.URL, playback.BackupURL,
					)
				}
			} else {
				log.Println(helpMsg)
			}
		} else {
			switch cmd[0] {
			case "listall":
				handleQuery(ctx, int(uid), -1)
			case "list10":
				handleQuery(ctx, int(uid), 10)
			case "updateall":
				handleUpdate(ctx, int(uid), -1)
			case "update10":
				handleUpdate(ctx, int(uid), 10)
			default:
				log.Println(helpMsg)
			}
		}
	}
	err = scanner.Err()
	checkErr(err)
}

// 获取指定liveID的playback
func getPlayback(liveID string) (playback *acfundanmu.Playback, err error) {
	for retry := 0; retry < 3; retry++ {
		playback, err = ac.GetPlayback(liveID)
		if err != nil {
			//log.Printf("获取liveID为 %s 的playback出现错误：%+v", liveID, err)
			if retry == 2 {
				return nil, fmt.Errorf("获取liveID为 %s 的playback失败：%w", liveID, err)
			}
			//log.Println("尝试重新获取playback")
		} else {
			break
		}
		time.Sleep(10 * time.Second)
	}

	if playback.URL != "" {
		aliURL, txURL := playback.Distinguish()
		if aliURL != "" && txURL != "" {
			playback.URL = aliURL
			playback.BackupURL = txURL
		} else {
			log.Printf("无法获取liveID为 %s 的阿里云录播链接或腾讯云录播链接", liveID)
		}
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

	db, err = sql.Open("sqlite", dbFile)
	checkErr(err)
	defer db.Close()
	err = db.Ping()
	checkErr(err)
	_, err = db.ExecContext(ctx, createTable)
	checkErr(err)
	_, err = db.ExecContext(ctx, createLiveIDIndex)
	checkErr(err)
	_, err = db.ExecContext(ctx, createUIDIndex)
	checkErr(err)

	insertStmt, err = db.PrepareContext(ctx, insertLive)
	checkErr(err)
	defer insertStmt.Close()
	updateStmt, err = db.PrepareContext(ctx, updatePlayback)
	checkErr(err)
	defer updateStmt.Close()

	ac, err = acfundanmu.NewAcFunLive()
	checkErr(err)
	go handleInput(childCtx)

	oldList := make(map[string]*live)
Loop:
	for {
		select {
		case <-childCtx.Done():
			break Loop
		default:
			var newList map[string]*live
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
					insert(ctx, l)
				}
			}

			for _, l := range oldList {
				if _, ok := newList[l.liveID]; !ok {
					// liveID对应的直播结束
					go func(l *live) {
						defer livePool.Put(l)
						time.Sleep(10 * time.Second)
						playback, err := getPlayback(l.liveID)
						if err != nil {
							log.Printf("获取 %s（%d） 的liveID为 %s 的playback出现错误：%+v", l.name, l.uid, l.liveID, err)
							return
						}
						if playback.URL == "" && playback.BackupURL == "" {
							log.Printf("录播链接为空，无法获取 %s（%d） 的liveID为 %s 的录播链接", l.name, l.uid, l.liveID)
							return
						}
						insert(ctx, l)
						update(ctx, l.liveID, playback)
						// 需要获取完整的录播链接
						const loopNum = 30
						for i := 0; i < loopNum; i++ {
							time.Sleep(30 * time.Minute)
							playback, err = getPlayback(l.liveID)
							if err != nil {
								log.Printf("获取 %s（%d） 的liveID为 %s 的playback出现错误：%+v", l.name, l.uid, l.liveID, err)
								return
							}
							if strings.Contains(playback.URL, ".0-0.0") {
								update(ctx, l.liveID, playback)
								return
							}
							if i == loopNum-1 {
								log.Printf("无法获取 %s（%d） 的liveID为 %s 的完整录播链接，请自行更新", l.name, l.uid, l.liveID)
							}
						}
					}(l)
				} else {
					livePool.Put(l)
				}
			}

			oldList = newList
			time.Sleep(20 * time.Second)
		}
	}
}
