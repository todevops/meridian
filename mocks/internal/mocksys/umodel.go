package mocksys

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// umEntity 是 UModel EntityStore 的实体条目：属性自由 JSON + 保活秒数 + 首末观测时间。
// Dead 为保活过期墓碑标记（超 keep_alive_seconds 未更新即置真），实体保留可查不物理删除。
type umEntity struct {
	Set              string         `json:"set"`
	PK               string         `json:"pk"`
	Attrs            map[string]any `json:"attrs"`
	KeepAliveSeconds int            `json:"keep_alive_seconds"`
	FirstSeen        time.Time      `json:"first_seen"`
	LastSeen         time.Time      `json:"last_seen"`
	Dead             bool           `json:"dead"`
}

// umLink 是实体间关联（EntitySetLink）：同 (set,src_pk,dst_pk,link_type) 重复写入按 upsert 去重。
type umLink struct {
	Set       string    `json:"set"`
	SrcPK     string    `json:"src_pk"`
	DstPK     string    `json:"dst_pk"`
	LinkType  string    `json:"link_type"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// umSetStats 是 /stats 中单个 EntitySet 的计数。
type umSetStats struct {
	Entities int `json:"entities"`
	Links    int `json:"links"`
}

// umState 是 UModel mock 的内存态：实体/关联按 EntitySet 分桶，upsert_total 供事件计数断言。
type umState struct {
	mu          sync.RWMutex
	entities    map[string]map[string]*umEntity // set -> pk -> 实体
	links       map[string]map[string]*umLink   // set -> src|dst|type -> 关联
	upsertTotal int                             // 实体 upsert 累计次数（F-073 事件驱动写入断言用）
}

// umLinkKey 生成关联去重键。
func umLinkKey(src, dst, typ string) string { return src + "|" + dst + "|" + typ }

// newUModel 构建 UModel EntityStore mock（:19011）：
// Authorization: Bearer 非空校验（空则 401）；纯内存态，无 fixture。
// 写入契约对齐开源 UModel REST 形态：实体 PUT upsert + 批量关联 PUT，
// 读取契约：实体列表（含保活墓碑）+ graph/match 多跳遍历 + stats 计数。
func newUModel() (http.Handler, error) {
	st := &umState{
		entities: map[string]map[string]*umEntity{},
		links:    map[string]map[string]*umLink{},
	}

	mux := http.NewServeMux()
	// 实体 upsert：body 为属性 JSON，keep_alive_seconds 为保留字段（其余原样存为 attrs）。
	mux.HandleFunc("PUT /api/v1/entitysets/{set}/entities/{pk}", func(w http.ResponseWriter, r *http.Request) {
		set, pk := r.PathValue("set"), r.PathValue("pk")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("请求体不是合法 JSON: %v", err)})
			return
		}
		keepAlive := 0
		if v, ok := body["keep_alive_seconds"]; ok {
			f, ok := v.(float64)
			if !ok || f < 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "keep_alive_seconds 必须是非负数字"})
				return
			}
			keepAlive = int(f)
			delete(body, "keep_alive_seconds")
		}
		now := time.Now().UTC()
		st.mu.Lock()
		bucket := st.entityBucket(set)
		ent, ok := bucket[pk]
		if !ok {
			ent = &umEntity{Set: set, PK: pk, FirstSeen: now}
			bucket[pk] = ent
		}
		ent.Attrs = body
		ent.KeepAliveSeconds = keepAlive
		ent.LastSeen = now
		st.upsertTotal++
		st.mu.Unlock()
		writeJSON(w, http.StatusOK, ent)
	})
	// 关联批量 upsert：body 为 [{src_pk,dst_pk,link_type}] 数组。
	mux.HandleFunc("PUT /api/v1/entitysets/{set}/links", func(w http.ResponseWriter, r *http.Request) {
		set := r.PathValue("set")
		var in []struct {
			SrcPK    string `json:"src_pk"`
			DstPK    string `json:"dst_pk"`
			LinkType string `json:"link_type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("请求体不是合法 JSON 数组: %v", err)})
			return
		}
		now := time.Now().UTC()
		st.mu.Lock()
		bucket := st.linkBucket(set)
		for i, l := range in {
			if strings.TrimSpace(l.SrcPK) == "" || strings.TrimSpace(l.DstPK) == "" || strings.TrimSpace(l.LinkType) == "" {
				st.mu.Unlock()
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("第 %d 条关联的 src_pk/dst_pk/link_type 不能为空", i)})
				return
			}
			key := umLinkKey(l.SrcPK, l.DstPK, l.LinkType)
			if link, ok := bucket[key]; ok {
				link.LastSeen = now
			} else {
				bucket[key] = &umLink{Set: set, SrcPK: l.SrcPK, DstPK: l.DstPK, LinkType: l.LinkType, FirstSeen: now, LastSeen: now}
			}
		}
		st.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]int{"upserted": len(in)})
	})
	// 实体列表：?keep_alive=false 关闭墓碑判定（全部 dead=false 的原始视图），默认开启。
	mux.HandleFunc("GET /api/v1/entitysets/{set}/entities", func(w http.ResponseWriter, r *http.Request) {
		evalTTL := r.URL.Query().Get("keep_alive") != "false"
		st.mu.RLock()
		out := make([]*umEntity, 0, len(st.entities[r.PathValue("set")]))
		for _, ent := range st.entities[r.PathValue("set")] {
			cp := *ent
			if evalTTL {
				cp.Dead = ent.expired(time.Now().UTC())
			}
			out = append(out, &cp)
		}
		st.mu.RUnlock()
		sort.Slice(out, func(i, j int) bool { return out[i].PK < out[j].PK })
		writeJSON(w, http.StatusOK, out)
	})
	// 图遍历：?src=<pk>[&set=<set>][&depth=N]（depth 缺省 1，上限 10）；
	// 沿关联双向遍历（实体图邻居查询语义），返回可达实体与途经关联。
	mux.HandleFunc("GET /api/v1/graph/match", func(w http.ResponseWriter, r *http.Request) {
		src := strings.TrimSpace(r.URL.Query().Get("src"))
		if src == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "src 查询参数不能为空"})
			return
		}
		depth := intQuery(r, "depth", 1)
		if depth < 1 {
			depth = 1
		}
		if depth > 10 {
			depth = 10
		}
		setFilter := strings.TrimSpace(r.URL.Query().Get("set"))
		now := time.Now().UTC()

		st.mu.RLock()
		defer st.mu.RUnlock()
		// 起点实体可能跨 set 同名：收集全部匹配的 (set,pk) 作为种子。
		type nodeKey struct{ set, pk string }
		visited := map[nodeKey]bool{}
		var frontier []nodeKey
		for set, bucket := range st.entities {
			if setFilter != "" && set != setFilter {
				continue
			}
			if _, ok := bucket[src]; ok {
				k := nodeKey{set, src}
				visited[k] = true
				frontier = append(frontier, k)
			}
		}
		if len(frontier) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("实体 %q 不存在", src)})
			return
		}
		linkSeen := map[string]*umLink{} // set|src|dst|type -> 关联（去重）
		for hop := 0; hop < depth && len(frontier) > 0; hop++ {
			var next []nodeKey
			for _, cur := range frontier {
				// 关联挂在写入时的 set 桶下、可跨 set 指向实体（如 host→db_instance），
				// 故遍历全部关联桶按 pk 匹配，对端实体按 pk 在全部实体桶中定位。
				for linkSet, bucket := range st.links {
					for _, link := range bucket {
						var peer string
						switch cur.pk {
						case link.SrcPK:
							peer = link.DstPK
						case link.DstPK:
							peer = link.SrcPK
						default:
							continue
						}
						linkSeen[linkSet+"|"+umLinkKey(link.SrcPK, link.DstPK, link.LinkType)] = link
						for entitySet, entities := range st.entities {
							if _, ok := entities[peer]; !ok {
								continue
							}
							k := nodeKey{entitySet, peer}
							if !visited[k] {
								visited[k] = true
								next = append(next, k)
							}
						}
					}
				}
			}
			frontier = next
		}
		entities := make([]*umEntity, 0, len(visited))
		for k := range visited {
			ent := st.entities[k.set][k.pk]
			cp := *ent
			cp.Dead = ent.expired(now)
			entities = append(entities, &cp)
		}
		sort.Slice(entities, func(i, j int) bool {
			if entities[i].Set != entities[j].Set {
				return entities[i].Set < entities[j].Set
			}
			return entities[i].PK < entities[j].PK
		})
		links := make([]*umLink, 0, len(linkSeen))
		for _, link := range linkSeen {
			links = append(links, link)
		}
		sort.Slice(links, func(i, j int) bool {
			if links[i].Set != links[j].Set {
				return links[i].Set < links[j].Set
			}
			return umLinkKey(links[i].SrcPK, links[i].DstPK, links[i].LinkType) < umLinkKey(links[j].SrcPK, links[j].DstPK, links[j].LinkType)
		})
		writeJSON(w, http.StatusOK, map[string]any{"entities": entities, "links": links})
	})
	// 计数：实体/关联总量、实体 upsert 累计、按 EntitySet 分桶计数（对账断言用）。
	mux.HandleFunc("GET /api/v1/stats", func(w http.ResponseWriter, r *http.Request) {
		st.mu.RLock()
		defer st.mu.RUnlock()
		perSet := map[string]umSetStats{}
		entityCount, linkCount := 0, 0
		for set, bucket := range st.entities {
			s := perSet[set]
			s.Entities = len(bucket)
			entityCount += s.Entities
			perSet[set] = s
		}
		for set, bucket := range st.links {
			s := perSet[set]
			s.Links = len(bucket)
			linkCount += s.Links
			perSet[set] = s
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"entity_count": entityCount,
			"link_count":   linkCount,
			"upsert_total": st.upsertTotal,
			"per_set":      perSet,
		})
	})
	return umodelAuth(mux), nil
}

// expired 判定实体是否保活过期（keep_alive_seconds>0 且距今超出即过期）。
func (e *umEntity) expired(now time.Time) bool {
	return e.KeepAliveSeconds > 0 && now.Sub(e.LastSeen) > time.Duration(e.KeepAliveSeconds)*time.Second
}

// entityBucket 取（或建）指定 EntitySet 的实体桶；调用方须持写锁。
func (st *umState) entityBucket(set string) map[string]*umEntity {
	if st.entities[set] == nil {
		st.entities[set] = map[string]*umEntity{}
	}
	return st.entities[set]
}

// linkBucket 取（或建）指定 EntitySet 的关联桶；调用方须持写锁。
func (st *umState) linkBucket(set string) map[string]*umLink {
	if st.links[set] == nil {
		st.links[set] = map[string]*umLink{}
	}
	return st.links[set]
}

// umodelAuth 对整个路由做 Bearer 非空校验（token 不校验具体值，缺失即 401）。
func umodelAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearerToken(r) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing bearer token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
