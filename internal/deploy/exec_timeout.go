package deploy

import "time"

// execTimeout 是单台部署里**每条命令步骤**的执行超时(防一台挂死拖垮整次部署;失败不连累其它机)。
// 大文件上传不适用它 —— 那是流式传输,额度按产物体积另算(见 release.go uploadTimeout)。
//
// 单独一个文件:命令步骤 / 上传后的落位 / 健康探测 / 镜像编排都要用它,留在 deploy.go 里就成了
// 跨文件引用的公共常量。
const execTimeout time.Duration = 60 * time.Second

// imagePullTimeout 是 image 部署里 `docker pull` 那一步的超时,刻意与上面的 60s 脱钩。
//
// 命令步骤用 60s 是有意的(一条 mv 跑一分钟 = 这台机有问题,早点失败)。拉镜像不适用这条判断:
// 耗时由镜像体积和仓库带宽决定,与「目标机是否挂死」无关。同一条链路在别处早已给足额度
// (httpapi 的 imagePullTimeout、本包的 composeUpTimeout 都是 8 分钟),唯独这里停在 60s,
// 表现是首次部署大镜像必报「部署执行超时」,而第二次(镜像已在本地)秒过 —— 让人以为坏在切换环节。
const imagePullTimeout time.Duration = 8 * time.Minute
