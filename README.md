# acfunlivedb
保存AcFun直播部分数据

### 编译安装
```
go install github.com/orzogc/acfunlivedb@master
```

### 运行依赖
* sqlite3
* 支持Linux、macOS、Windows和FreeBSD

### 用法
运行后会在本程序所在文件夹生成 `acfunlive.db` 数据库文件，本程序会自动爬取从运行时间开始的AcFun所有直播间的部分直播数据并保存到数据库里。

由于录播链接的有效性有时间限制，超时后需要重新查询，所以本程序不再自动更新录播链接，需要用`getplayback`命令自行手动查询。

运行时可以输入以下命令：

`listall 主播的uid` 列出数据库里指定主播的所有直播数据，按照开播时间降序排列

`list10 主播的uid` 列出数据库里指定主播最近10次直播的数据，按照开播时间降序排列

`getplayback liveID` 根据直播的`liveID`查询AcFun官方的录播链接，注意不是所有直播都能查询到对应的录播链接

`quit` 结束运行
