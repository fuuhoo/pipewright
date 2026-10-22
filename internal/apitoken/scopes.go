// 令牌范围的持久化:两列都存 JSON 数组文本,空数组表示不限。
//
// 为什么用 JSON 列而不是 api_token_tools 关联表:
//   - 范围只在「校验令牌 -> 铸 Principal」这一条读路径上整体取用,从不按工具名反查
//     「哪些令牌暴露了它」,而那是关联表的唯一价值,这里用不上。
//   - 存量表已有先例:certificates.sans 同样是 JSON 数组文本。
//   - 一次签发要同时改工具与分组两个范围,单行 UPDATE 比两张表的差量写入更难写错。
//
// 三态口径(全站统一,别在调用方再造第四态):
//   - 空数组:不限,等价于没有这层收窄;迁移 0078 之前的存量令牌全是这一态。
//   - 非空数组:只允许列出的这些值。
//   - 「一个都不给」由 HTTP 签发接口当场拒签,不落库:一把没有工具的令牌既占着列表,
//     又只能在 tools/call 里回「未知工具」,比不签发更难排查。

package apitoken

import (
	"encoding/json"
	"strings"
)

// marshalScope 归一化后编码为 JSON 数组文本。空集合必须写成 [] 而不是 null:
// 库里只留一种「不限」的字面形态,读侧才不必分辨两种空。
func marshalScope(items []string) (string, error) {
	out := normalizeScope(items)
	if len(out) == 0 {
		return "[]", nil
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// unmarshalScope 读回并归一化。列里是空串(而非空数组)时同样按「不限」处理:
// 这一列只由本包写入,正常路径写不出空串,不值得让一次合法调用败在编码细节上。
func unmarshalScope(raw string) ([]string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	var items []string
	if err := json.Unmarshal([]byte(s), &items); err != nil {
		return nil, err
	}
	return normalizeScope(items), nil
}

// normalizeScope 是两向共用的清洗(去首尾空白、丢空项、按首次出现去重),
// 保证「签发时写进去的」和「读出来判定的」是同一份东西。
func normalizeScope(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, it := range items {
		v := strings.TrimSpace(it)
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
