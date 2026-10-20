// h2h3probe 从当前网络实测智谱 open.bigmodel.cn 的 HTTP/1.1、HTTP/2、HTTP/3 可用性与延迟。
// 每轮各发一次真实 chat/completions 请求(max_tokens=1), 输出逐次结果与汇总判定。
//
// 用法:
//
//	ZHIPU_KEY=<key> ./h2h3probe [-n 8] [-model glm-4-flash]
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/quic-go/quic-go/http3"
)

const (
	baseURL = "https://open.bigmodel.cn"
	apiPath = "/api/paas/v4/chat/completions"
)

func newRoundTrippers() map[string]http.RoundTripper {
	// h1: 显式禁用 h2 升级, 复现当前 fasthttp 生产路径的协议形态。
	h1 := &http.Transport{
		TLSClientConfig: &tls.Config{},
		TLSNextProto:    map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	h2 := &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig:   &tls.Config{NextProtos: []string{"h2", "http/1.1"}},
	}
	h3 := &http3.Transport{TLSClientConfig: &tls.Config{NextProtos: []string{"h3"}}}
	return map[string]http.RoundTripper{
		"HTTP/1.1": h1,
		"HTTP/2":   h2,
		"HTTP/3":   h3,
	}
}

func once(rt http.RoundTripper, key, model string, timeout time.Duration) (proto string, elapsed time.Duration, err error) {
	body, _ := jsonMarshal(model)
	req, _ := http.NewRequest(http.MethodPost, baseURL+apiPath, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req = req.WithContext(ctx)
	start := time.Now()
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return "", time.Since(start), err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	proto = resp.Proto
	elapsed = time.Since(start)
	if resp.StatusCode != http.StatusOK {
		return proto, elapsed, fmt.Errorf("status %d", resp.StatusCode)
	}
	return proto, elapsed, nil
}

func jsonMarshal(model string) ([]byte, error) {
	return []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":1}`, model)), nil
}

func summary(name string, protos []string, rts []time.Duration, errs []error) {
	sort.Slice(rts, func(i, j int) bool { return rts[i] < rts[j] })
	negotiated := map[string]int{}
	for _, p := range protos {
		negotiated[p]++
	}
	fmt.Printf("  汇总: 成功 %d/%d", len(rts), len(rts)+len(errs))
	if len(rts) > 0 {
		avg := time.Duration(0)
		for _, d := range rts {
			avg += d
		}
		avg /= time.Duration(len(rts))
		fmt.Printf(" | 延迟 min=%v avg=%v max=%v", rts[0], avg, rts[len(rts)-1])
	}
	fmt.Print(" | 协商: ")
	if len(negotiated) == 0 {
		fmt.Print("无")
	}
	for p, n := range negotiated {
		fmt.Printf("%s×%d ", p, n)
	}
	fmt.Println()
	if len(errs) > 0 {
		kinds := map[string]int{}
		for _, err := range errs {
			kinds[classify(err)]++
		}
		fmt.Print("  失败分类: ")
		for k, n := range kinds {
			fmt.Printf("%s×%d ", k, n)
		}
		fmt.Println()
	}
}

func classify(err error) string {
	s := err.Error()
	for _, pat := range []struct {
		needle string
		label  string
	}{
		{"no recent network activity", "UDP黑洞/QUIC超时"},
		{"timeout", "超时(丢包或SYN无应答)"},
		{"connection refused", "端口拒绝"},
		{"tls:", "TLS握手失败"},
		{"status 4", "上游4xx"},
		{"status 5", "上游5xx"},
	} {
		if bytes.Contains([]byte(s), []byte(pat.needle)) {
			return pat.label
		}
	}
	return "其他:" + s
}

func main() {
	key := flag.String("key", os.Getenv("ZHIPU_KEY"), "智谱 API key (或环境变量 ZHIPU_KEY)")
	n := flag.Int("n", 8, "每协议探测轮数")
	model := flag.String("model", "glm-4-flash", "探测用模型")
	timeout := flag.Duration("timeout", 15*time.Second, "单请求超时")
	flag.Parse()
	if *key == "" {
		fmt.Fprintln(os.Stderr, "缺少 key: 用 -key 或 ZHIPU_KEY 环境变量")
		os.Exit(1)
	}
	rts := newRoundTrippers()
	names := []string{"HTTP/1.1", "HTTP/2", "HTTP/3"}
	fmt.Printf("目标 %s%s 模型 %s 轮数 %d 超时 %v\n\n", baseURL, apiPath, *model, *n, *timeout)
	verdict := map[string]bool{}
	for _, name := range names {
		fmt.Printf("== %s ==\n", name)
		var protos []string
		var okRts []time.Duration
		var errs []error
		for i := 1; i <= *n; i++ {
			proto, elapsed, err := once(rts[name], *key, *model, *timeout)
			if err != nil {
				errs = append(errs, err)
				fmt.Printf("  [%d] FAIL %v (%v) %v\n", i, elapsed, proto, classify(err))
				continue
			}
			protos = append(protos, proto)
			okRts = append(okRts, elapsed)
			fmt.Printf("  [%d] ok %v %v\n", i, elapsed, proto)
			if proto == name || proto == name+".0" {
				verdict[name] = true
			}
		}
		summary(name, protos, okRts, errs)
		fmt.Println()
	}
	fmt.Println("== 判定 ==")
	for _, name := range names {
		if verdict[name] {
			fmt.Printf("  %s: 可用\n", name)
		} else {
			fmt.Printf("  %s: 不可用\n", name)
		}
	}
}
