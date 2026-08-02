// 标签规范化工具：云厂商标签（map）→ CI 字符串属性。
package record

import (
	"sort"
	"strings"
)

// FormatTags 把键值对标签规范化为 "k1=v1,k2=v2" 字符串（键排序保证确定性），
// 供云资源标签写入模型的字符串类型属性。
func FormatTags(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+tags[k])
	}
	return strings.Join(parts, ",")
}
