package mocksys

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

// newAliyun 构建阿里云 mock（:19005）：
// 任意方法、任意路径均返回 ECS 实例数组 fixture（官方 DescribeInstances 字段风格）。
func newAliyun() (http.Handler, error) {
	raw, err := readFixture("aliyun-ecs.json")
	if err != nil {
		return nil, fmt.Errorf("读取 aliyun-ecs.json 失败: %w", err)
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("aliyun-ecs.json 不是合法 JSON")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeRawJSON(w, http.StatusOK, raw)
	})
	return mux, nil
}

// volcTag 是火山引擎资源标签（Key/Value 结构）。
type volcTag struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// volcResourceFixture 是 fixture 中的资源条目：Configuration 以对象书写便于维护。
type volcResourceFixture struct {
	ResourceType  string          `json:"ResourceType"`
	ResourceID    string          `json:"ResourceId"`
	Configuration json.RawMessage `json:"Configuration"`
	Tags          []volcTag       `json:"Tags"`
}

// volcResource 是 CloudControl ListResources 响应中的资源条目：
// Configuration 按官方协议转为 JSON 字符串。
type volcResource struct {
	ResourceType  string    `json:"ResourceType"`
	ResourceID    string    `json:"ResourceId"`
	Configuration string    `json:"Configuration"`
	Tags          []volcTag `json:"Tags"`
}

// newVolcengine 构建火山引擎 CloudControl mock（:19006）：
// POST /?Action=ListResources&Version=2021-01-01 返回
// {ResponseMetadata:{RequestId,...},Result:{Resources:[...],TotalCount}}。
func newVolcengine() (http.Handler, error) {
	raw, err := readFixture("volcengine-resources.json")
	if err != nil {
		return nil, fmt.Errorf("读取 volcengine-resources.json 失败: %w", err)
	}
	var fixture struct {
		Resources []volcResourceFixture `json:"Resources"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		return nil, fmt.Errorf("解析 volcengine-resources.json 失败: %w", err)
	}

	resources := make([]volcResource, 0, len(fixture.Resources))
	for _, res := range fixture.Resources {
		// 压实成单行 JSON 字符串，与官方线上返回形态一致
		var compact bytes.Buffer
		if err := json.Compact(&compact, res.Configuration); err != nil {
			return nil, fmt.Errorf("资源 %s 的 Configuration 非法: %w", res.ResourceID, err)
		}
		resources = append(resources, volcResource{
			ResourceType:  res.ResourceType,
			ResourceID:    res.ResourceID,
			Configuration: compact.String(),
			Tags:          res.Tags,
		})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("Action")
		version := r.URL.Query().Get("Version")
		if action != "ListResources" {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ResponseMetadata": map[string]any{
					"RequestId": requestID(),
					"Error":     map[string]string{"Code": "InvalidAction", "Message": fmt.Sprintf("不支持的 Action: %q（mock 仅实现 ListResources）", action)},
				},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ResponseMetadata": map[string]any{
				"RequestId": requestID(),
				"Action":    action,
				"Version":   version,
				"Service":   "cloudcontrol",
				"Region":    "cn-beijing",
			},
			"Result": map[string]any{
				"Resources":  resources,
				"TotalCount": len(resources),
			},
		})
	})
	return mux, nil
}
