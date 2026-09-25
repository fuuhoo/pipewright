package deploy

import "time"

// execTimeout 是单台部署里**每条命令步骤**的执行超时(防一台挂死拖垮整次部署;失败不连累其它机)。
// 大文件上传不适用它 —— 那是流式传输,额度按产物体积另算(见 release.go uploadTimeout)。
//
// 单独一个文件:命令步骤 / 上传后的落位 / 健康探测 / 镜像编排都要用它,留在 deploy.go 里就成了
// 跨文件引用的公共常量。
const execTimeout time.Duration = 60 * time.Second
