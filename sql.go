package main

import (
	"context"
	"database/sql"

	"github.com/orzogc/acfundanmu"
	_ "modernc.org/sqlite"
)

// 新建table
const createTable = `CREATE TABLE IF NOT EXISTS acfunlive (
	liveID TEXT PRIMARY KEY,
	uid INTEGER NOT NULL,
	name TEXT NOT NULL,
	streamName TEXT NOT NULL UNIQUE,
	startTime INTEGER NOT NULL,
	title TEXT NOT NULL,
	duration INTEGER NOT NULL,
	playbackURL TEXT NOT NULL,
	backupURL TEXT NOT NULL
);
`

// 插入live
const insertLive = `INSERT OR IGNORE INTO acfunlive
	(liveID, uid, name, streamName, startTime, title, duration, playbackURL, backupURL)
	VALUES
	(?, ?, ?, ?, ?, ?, ?, ?, ?);
`

// 更新录播链接
const updatePlayback = `UPDATE acfunlive SET
	duration = ?,
	playbackURL = ?,
	backupURL = ?
	WHERE liveID = ?;
`

// 根据uid查询
const selectUID = `SELECT * FROM acfunlive
	WHERE uid = ?
	ORDER BY startTime DESC
	LIMIT ?;
`

const (
	selectLiveID      = `SELECT uid FROM acfunlive WHERE liveID = ?;`                   // 根据liveID查询
	createLiveIDIndex = `CREATE INDEX IF NOT EXISTS liveIDIndex ON acfunlive (liveID);` // 生成liveID的index
	createUIDIndex    = `CREATE INDEX IF NOT EXISTS uidIndex ON acfunlive (uid);`       // 生成uid的index
)

var (
	db               *sql.DB
	insertStmt       *sql.Stmt
	updateStmt       *sql.Stmt
	selectUIDStmt    *sql.Stmt
	selectLiveIDStmt *sql.Stmt
)

// 插入live
func insert(ctx context.Context, l *live) {
	_, err := insertStmt.ExecContext(ctx,
		l.liveID, l.uid, l.name, l.streamName, l.startTime, l.title, l.duration, l.playbackURL, l.backupURL,
	)
	checkErr(err)
}

// 更新录播链接
func update(ctx context.Context, liveID string, playback *acfundanmu.Playback) {
	_, err := updateStmt.ExecContext(ctx,
		playback.Duration, playback.URL, playback.BackupURL, liveID,
	)
	checkErr(err)
}

// 查询liveID的数据是否存在
func queryExist(ctx context.Context, liveID string) bool {
	var uid int
	err := selectLiveIDStmt.QueryRowContext(ctx, liveID).Scan(&uid)
	if err == nil {
		return true
	}
	return false
}
