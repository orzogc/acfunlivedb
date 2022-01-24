package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
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

const userAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/97.0.4692.99 Safari/537.36"

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
	liveCutNum  int    // 直播剪辑编号
}

var client = &fasthttp.Client{
	MaxIdleConnDuration: 90 * time.Second,
	ReadTimeout:         10 * time.Second,
	WriteTimeout:        10 * time.Second,
}

var (
	liveListParserPool fastjson.ParserPool
	liveCutParserPool  fastjson.ParserPool
	quit               = make(chan struct{})
	ac                 *acfundanmu.AcFunLive
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

// 尝试运行，三次出错后结束运行
func runThrice(t time.Duration, f func() error) error {
	var err error
	for retry := 0; retry < 3; retry++ {
		if err = f(); err != nil {
			log.Printf("%v", err)
		} else {
			return nil
		}
		time.Sleep(t)
	}
	return fmt.Errorf("运行三次都出现错误：%v", err)
}

// 获取正在直播的直播间列表数据
func fetchLiveList() (list map[string]*live, e error) {
	defer func() {
		if err := recover(); err != nil {
			e = fmt.Errorf("fetchLiveList() error: %v", err)
		}
	}()

	const liveListURL = "https://live.acfun.cn/api/channel/list?count=%d&pcursor=0"
	//const liveListURL = "https://live.acfun.cn/rest/pc-direct/live/channel"

	p := liveListParserPool.Get()
	defer liveListParserPool.Put(p)
	var v *fastjson.Value

	for count := 10000; count < 1e9; count *= 10 {
		req := fasthttp.AcquireRequest()
		defer fasthttp.ReleaseRequest(req)
		resp := fasthttp.AcquireResponse()
		defer fasthttp.ReleaseResponse(resp)
		req.SetRequestURI(fmt.Sprintf(liveListURL, count))
		req.Header.SetMethod(fasthttp.MethodGet)
		req.Header.SetUserAgent(userAgent)
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
		v = v.Get("channelListData")
		if !v.Exists("result") || v.GetInt("result") != 0 {
			panic(fmt.Errorf("获取正在直播的直播间列表失败，响应为 %s", string(body)))
		}
		if string(v.GetStringBytes("pcursor")) == "no_more" {
			break
		}
		if count == 1e8 {
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
		l.liveCutNum = 0
		list[l.liveID] = l
	}

	return list, nil
}

// 获取直播剪辑编号
func fetchLiveCut(uid int, liveID string) (num int, e error) {
	defer func() {
		if err := recover(); err != nil {
			num = 0
			e = fmt.Errorf("fetchLiveCut() error: %v", err)
		}
	}()

	const liveCutInfoURL = "https://live.acfun.cn/rest/pc-direct/live/getLiveCutInfo?authorId=%d&liveId=%s"

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	req.SetRequestURI(fmt.Sprintf(liveCutInfoURL, uid, liveID))
	req.Header.SetMethod(fasthttp.MethodGet)
	req.Header.SetUserAgent(userAgent)
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

	p := liveCutParserPool.Get()
	defer liveCutParserPool.Put(p)
	v, err := p.ParseBytes(body)
	checkErr(err)
	if !v.Exists("result") || v.GetInt("result") != 0 {
		panic(fmt.Errorf("获取uid为 %d 的主播的liveID为 %s 的直播剪辑信息失败，响应为 %s", uid, liveID, string(body)))
	}

	status := v.GetInt("liveCutStatus")
	if status != 1 {
		return 0, nil
	}
	url := string(v.GetStringBytes("liveCutUrl"))
	re := regexp.MustCompile(`/[0-9]+`)
	nums := re.FindAllString(url, -1)
	if len(nums) != 1 {
		panic(fmt.Errorf("获取uid为 %d 的主播的liveID为 %s 的直播剪辑编号失败，响应为 %s", uid, liveID, string(body)))
	}
	num, err = strconv.Atoi(nums[0][1:])
	checkErr(err)

	return num, nil
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
	hasUID := false
	for rows.Next() {
		hasUID = true
		err = rows.Scan(&l.liveID, &l.uid, &l.name, &l.streamName, &l.startTime, &l.title, &l.duration, &l.playbackURL, &l.backupURL, &l.liveCutNum)
		checkErr(err)
		fmt.Printf("开播时间：%s 主播uid：%d 昵称：%s 直播标题：%s liveID: %s streamName: %s 直播时长：%s 直播剪辑编号：%d\n",
			startTime(l.startTime), l.uid, l.name, l.title, l.liveID, l.streamName, duration(l.duration), l.liveCutNum,
		)
	}
	err = rows.Err()
	checkErr(err)
	if !hasUID {
		log.Printf("没有uid为 %d 的主播的记录", uid)
	}
}

// 处理输入
func handleInput(ctx context.Context) {
	const helpMsg = `请输入"listall 主播的uid"、"list10 主播的uid"、"getplayback liveID"或"quit"`

	var err error
	selectUIDStmt, err = db.PrepareContext(ctx, selectUID)
	checkErr(err)
	defer selectUIDStmt.Close()

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		cmd := strings.Fields(scanner.Text())
		if len(cmd) == 0 {
			log.Println(helpMsg)
			continue
		}
		if len(cmd) == 1 {
			if cmd[0] == "quit" {
				quit <- struct{}{}
				break
			}
			log.Println(helpMsg)
			continue
		}
		switch cmd[0] {
		case "listall":
			for _, u := range cmd[1:] {
				uid, err := strconv.ParseUint(u, 10, 64)
				if err != nil {
					log.Println(helpMsg)
				} else {
					handleQuery(ctx, int(uid), -1)
				}
			}
		case "list10":
			for _, u := range cmd[1:] {
				uid, err := strconv.ParseUint(u, 10, 64)
				if err != nil {
					log.Println(helpMsg)
				} else {
					handleQuery(ctx, int(uid), 10)
				}
			}
		case "getplayback":
			log.Println("查询录播链接，请等待")
			for _, liveID := range cmd[1:] {
				playback, err := getPlayback(liveID)
				if err != nil {
					log.Println(err)
				} else {
					if playback.Duration != 0 {
						if queryExist(ctx, liveID) {
							updateLiveDuration(ctx, liveID, playback.Duration)
						}
					}
					log.Printf("liveID为 %s 的录播查询结果是：\n录播链接：%s\n录播备份链接：%s",
						liveID, playback.URL, playback.BackupURL,
					)
				}
			}
		default:
			log.Println(helpMsg)
		}
	}
	err = scanner.Err()
	checkErr(err)
}

// 获取指定liveID的playback
func getPlayback(liveID string) (playback *acfundanmu.Playback, err error) {
	err = runThrice(10*time.Second, func() error {
		playback, err = ac.GetPlayback(liveID)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("获取liveID为 %s 的playback失败：%w", liveID, err)
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

// 准备table
func prepare_table(ctx context.Context) {
	// 检查table是否存在
	row := db.QueryRowContext(ctx, checkTable)
	var n int
	err := row.Scan(&n)
	checkErr(err)
	if n == 0 {
		// table不存在
		_, err = db.ExecContext(ctx, createTable)
		checkErr(err)
	} else {
		// table存在，检查liveCutNum是否存在
		row = db.QueryRowContext(ctx, checkLiveCutNum)
		err = row.Scan(&n)
		checkErr(err)
		if n == 0 {
			// liveCutNum不存在，插入liveCutNum
			_, err = db.ExecContext(ctx, insertLiveCutNum)
			checkErr(err)
		}
	}
	_, err = db.ExecContext(ctx, createLiveIDIndex)
	checkErr(err)
	_, err = db.ExecContext(ctx, createUIDIndex)
	checkErr(err)
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
	_, err = db.ExecContext(ctx, setTimeout)
	checkErr(err)
	prepare_table(ctx)

	insertStmt, err = db.PrepareContext(ctx, insertLive)
	checkErr(err)
	defer insertStmt.Close()
	updateDurationStmt, err = db.PrepareContext(ctx, updateDuration)
	checkErr(err)
	defer updateDurationStmt.Close()
	selectLiveIDStmt, err = db.PrepareContext(ctx, selectLiveID)
	checkErr(err)
	defer selectLiveIDStmt.Close()

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
			err = runThrice(10*time.Second, func() error {
				newList, err = fetchLiveList()
				return err
			})
			if err != nil {
				panic("获取直播间列表数据出现过多错误")
			}

			if len(newList) == 0 {
				log.Println("没有人在直播")
			}

			for _, l := range newList {
				if _, ok := oldList[l.liveID]; !ok {
					// 新的liveID
					err = runThrice(time.Second, func() error {
						l.liveCutNum, err = fetchLiveCut(l.uid, l.liveID)
						return err
					})
					if err != nil {
						log.Printf("获取 %s（%d） 的liveID为 %s 的直播剪辑编号失败，放弃获取", l.name, l.uid, l.liveID)
						l.liveCutNum = 0
					}
					insert(ctx, l)
				}
			}

			for _, l := range oldList {
				if _, ok := newList[l.liveID]; !ok {
					// liveID对应的直播结束
					go func(l *live) {
						defer livePool.Put(l)
						time.Sleep(10 * time.Second)
						var summary *acfundanmu.Summary
						var err error
						err = runThrice(10*time.Second, func() error {
							summary, err = ac.GetSummary(l.liveID)
							return err
						})
						if err != nil {
							log.Printf("获取 %s（%d） 的liveID为 %s 的直播总结出现错误，放弃获取", l.name, l.uid, l.liveID)
							return
						}
						if summary.Duration == 0 {
							log.Printf("直播时长为0，无法获取 %s（%d） 的liveID为 %s 的直播时长", l.name, l.uid, l.liveID)
							return
						}
						if !queryExist(ctx, l.liveID) {
							insert(ctx, l)
						}
						updateLiveDuration(ctx, l.liveID, summary.Duration)
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
