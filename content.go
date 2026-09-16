package main

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type object = map[string]any

var defaultContent object

var legacyVideoURLs = map[string]string{}

func parseObject(data []byte) object {
	var v object
	if json.Unmarshal(data, &v) != nil || v == nil {
		return object{}
	}
	return v
}

func cloneObject(v object) object {
	b, _ := json.Marshal(v)
	return parseObject(b)
}

func asObject(v any) object {
	m, _ := v.(map[string]any)
	return m
}

func asArray(v any) []any {
	a, _ := v.([]any)
	return a
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func boolValue(v any, fallback bool) bool {
	b, ok := v.(bool)
	if !ok {
		return fallback
	}
	return b
}

func normalizeContent(input object) object {
	result := cloneObject(defaultContent)
	for _, key := range []string{"profile", "hero", "footer", "sections", "seo"} {
		if in := asObject(input[key]); in != nil {
			out := asObject(result[key])
			for k, v := range in {
				out[k] = v
			}
		}
	}
	if in := asObject(input["about"]); in != nil {
		out := asObject(result["about"])
		for k, v := range in {
			out[k] = v
		}
	} else if values := asArray(input["about"]); values != nil {
		parts := make([]string, 0, len(values))
		for _, v := range values {
			parts = append(parts, fmt.Sprint(v))
		}
		asObject(result["about"])["description"] = strings.Join(parts, "\n\n")
	}
	if _, ok := input["tools"].(string); ok {
		result["tools"] = input["tools"]
	}
	for _, key := range []string{"experiences", "caseStudies", "projects", "software", "socials"} {
		if a := asArray(input[key]); a != nil {
			result[key] = a
		}
	}
	projects := asArray(result["projects"])
	for i, raw := range projects {
		item := asObject(raw)
		if item == nil {
			continue
		}
		typeName := stringValue(item["type"])
		if typeName == "" {
			if strings.Contains(stringValue(item["category"]), "视频") {
				typeName = "video"
			} else {
				typeName = "photo"
			}
			item["type"] = typeName
		}
		if stringValue(item["id"]) == "" {
			h := sha1.Sum([]byte(fmt.Sprintf("%s-%d", fallbackString(stringValue(item["title"]), "item"), i)))
			item["id"] = "work-" + hex.EncodeToString(h[:])[:10]
		}
		if _, ok := item["description"]; !ok || item["description"] == nil {
			item["description"] = ""
		}
		if _, ok := item["externalUrl"]; !ok || item["externalUrl"] == nil {
			if typeName == "photo" {
				item["externalUrl"] = stringValue(item["link"])
			} else {
				item["externalUrl"] = ""
			}
		}
		if _, ok := item["videoUrl"]; !ok || item["videoUrl"] == nil {
			url := ""
			if typeName == "video" {
				url = legacyVideoURLs[stringValue(item["title"])]
				if url == "" && strings.Contains(strings.ToLower(stringValue(item["link"])), ".mp4") {
					url = stringValue(item["link"])
				}
			}
			item["videoUrl"] = url
		}
		delete(item, "link")
	}
	experiences := asArray(result["experiences"])
	for _, raw := range experiences {
		item := asObject(raw)
		if item == nil {
			continue
		}
		if stringValue(item["department"]) == "" {
			item["department"] = ""
		}
		if (stringValue(item["startDate"]) == "" || stringValue(item["endDate"]) == "") && stringValue(item["period"]) != "" {
			period := stringValue(item["period"])
			separator := ""
			for _, candidate := range []string{"—", "–", " - "} {
				if strings.Contains(period, candidate) {
					separator = candidate
					break
				}
			}
			if separator != "" {
				parts := strings.SplitN(period, separator, 2)
				if stringValue(item["startDate"]) == "" {
					item["startDate"] = strings.TrimSpace(parts[0])
				}
				if stringValue(item["endDate"]) == "" {
					item["endDate"] = strings.TrimSpace(parts[1])
				}
			} else if stringValue(item["startDate"]) == "" {
				item["startDate"] = period
			}
		}
		if _, ok := item["startDate"]; !ok {
			item["startDate"] = ""
		}
		if _, ok := item["endDate"]; !ok {
			item["endDate"] = ""
		}
		delete(item, "period")
	}
	return result
}

func fallbackString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func stableJSON(v any) string {
	switch value := v.(type) {
	case []any:
		parts := make([]string, len(value))
		for i, item := range value {
			parts[i] = stableJSON(item)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]any:
		keys := make([]string, 0, len(value))
		for k := range value {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, jsonString(k)+":"+stableJSON(value[k]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	default:
		b, _ := json.Marshal(value)
		return string(b)
	}
}

func jsonString(s string) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func contentHash(content object) string {
	sum := sha256.Sum256([]byte(stableJSON(normalizeContent(content))))
	return hex.EncodeToString(sum[:])
}

func validContent(v object) bool {
	return v != nil && asObject(v["profile"]) != nil && asObject(v["hero"]) != nil && asObject(v["about"]) != nil && asArray(v["experiences"]) != nil && asArray(v["caseStudies"]) != nil && asArray(v["projects"]) != nil
}

func projectValidationIssues(content object) []string {
	var issues []string
	for _, raw := range asArray(content["projects"]) {
		item := asObject(raw)
		if item == nil || !boolValue(item["published"], true) {
			continue
		}
		typeName := stringValue(item["type"])
		if typeName == "video" && stringValue(item["videoUrl"]) == "" {
			issues = append(issues, fallbackString(stringValue(item["title"]), "未命名作品")+"缺少视频地址")
		}
		if typeName == "photo" && stringValue(item["externalUrl"]) == "" {
			issues = append(issues, fallbackString(stringValue(item["title"]), "未命名作品")+"缺少外部链接")
		}
	}
	return issues
}
