// gate.go:同一台目标机一次只跑一段部署(多机扇出照旧并行)。
//
// 为什么必须有这道闸:每个远程操作(建目录 / 传字节 / 跑命令)都是一条独立的 SSH 连接,同一台机上
// 并发开三条时观察到的不是「快三倍」而是互相饿死 —— 三个部署任务同时打 35 服务器,一条 mkdir 能在
// 60s 后被自己的 ctx 掐死,而报错说的是「部署执行超时」,看着像目标机坏了。sshd 还会因未认证连接
// 过多直接拒掉后到的握手,报成「无法连接服务器」。串行以后账就对得上:68MB 传 39s、6.7MB 传 5s,
// 一条接一条全部通过。
//
// 用「容量 1 的通道」而不是 sync.Mutex:等锁也得能按 ctx 退出,否则目标机挂住时等待方跟着挂死。
package deploy

import "context"

// serverGate 取该机的执行权通道:serverID → 容量 1 的通道,懒建。不同机器各拿各的闸,互不阻塞
// (多机扇出照旧并行)。
func (s *service) serverGate(serverID string) chan struct{} {
	s.gatesMu.Lock()
	defer s.gatesMu.Unlock()
	if s.gates == nil {
		s.gates = make(map[string]chan struct{}, 4)
	}
	ch, ok := s.gates[serverID]
	if !ok {
		ch = make(chan struct{}, 1)
		s.gates[serverID] = ch
	}
	return ch
}

// gateTicketKey 把「本 ctx 已占住哪台机的执行权」记在 ctx 上,使闸可重入。
//
// 为什么必须可重入:排队的时间**不能算进操作自己的额度**。调用方一律先算好 60s(或按体积的
// 上传额度)再交给 exec/upload,若在操作层排队,等到的只剩残值 —— 日志就写成「部署执行超时」,
// 看着像目标机坏了。所以改成「一次单机部署 = 一段临界区」:入口处一次拿闸,区内每条命令都从
// 此刻重新计时(见 holdServer)。
type gateTicketKey struct{}

// holdServer 在 ctx 上占住该机的执行权,返回带凭证的 ctx 与释放函数;已在临界区内 → 原样透传
// (不重复排队)。失败时 ctx 不变、释放函数为空操作,调用方直接按人读错误上抛即可。
func (s *service) holdServer(ctx context.Context, serverID string) (context.Context, func(), error) {
	unlock, err := s.occupy(ctx, serverID)
	if err != nil {
		return ctx, func() {}, err
	}
	if held, _ := ctx.Value(gateTicketKey{}).(string); held == serverID {
		return ctx, unlock, nil
	}
	return context.WithValue(ctx, gateTicketKey{}, serverID), unlock, nil
}

// occupy 取回该机的执行权,返回释放函数;ctx 到期 / 取消则不占用并原样上抛 ctx 错误(由调用侧
// 映射成「超时 / 已取消」的人读)。排队要说得出口:否则日志里是一段没有任何解释的沉默。
func (s *service) occupy(ctx context.Context, serverID string) (func(), error) {
	if held, _ := ctx.Value(gateTicketKey{}).(string); held == serverID {
		return func() {}, nil
	}
	ch := s.serverGate(serverID)
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	default:
	}
	lg := cmdLogFrom(ctx)
	lg(cmdStreamStdout, "· 目标机上还有其它部署在跑,排队等它让位(同一台机串行,避免互相抢 SSH 把彼此饿死)")
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
