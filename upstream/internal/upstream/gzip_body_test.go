package upstream

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

// TestOutboundBodyGzipThreshold 小于阈值不压、超过阈值压且可解回原文。
func TestOutboundBodyGzipThreshold(t *testing.T) {
	c := &Client{GzipBody: true}
	small := bytes.Repeat([]byte("a"), gzipBodyThreshold-1)
	if out, enc := c.outboundBody(small); enc != "" || !bytes.Equal(out, small) {
		t.Fatalf("小于阈值不应压缩（enc=%q len=%d）", enc, len(out))
	}
	big := bytes.Repeat([]byte("上下文"), 30000) // 可压缩文本，>64KB
	out, enc := c.outboundBody(big)
	if enc != "gzip" {
		t.Fatalf("超过阈值应压缩，got enc=%q", enc)
	}
	if len(out) >= len(big) {
		t.Fatalf("压缩后应更小：%d >= %d", len(out), len(big))
	}
	zr, err := gzip.NewReader(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	back, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(back, big) {
		t.Fatal("解压后与原文不一致")
	}
}

// TestOutboundBodyDisabled 关闭开关时永不压缩（排障路径）。
func TestOutboundBodyDisabled(t *testing.T) {
	c := &Client{GzipBody: false}
	big := bytes.Repeat([]byte("上下文"), 30000)
	if _, enc := c.outboundBody(big); enc != "" {
		t.Fatalf("关闭后不应压缩，got %q", enc)
	}
}

// TestGzipUnsupported 只认 415 与「解析失败类」400。
func TestGzipUnsupported(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   bool
	}{
		{http.StatusUnsupportedMediaType, "", true},
		{http.StatusBadRequest, `{"msg":"Unmarshal chat params failed"}`, true},
		{http.StatusBadRequest, `{"msg":"invalid character '\x1f' looking for beginning of value"}`, true},
		{http.StatusBadRequest, `{"msg":"content-encoding not supported"}`, true},
		{http.StatusBadRequest, `{"msg":"model is required"}`, false},
		{http.StatusTooManyRequests, `{"code":6004}`, false},
		{http.StatusInternalServerError, `oops`, false},
	}
	for _, c := range cases {
		if got := gzipUnsupported(c.status, c.body); got != c.want {
			t.Errorf("gzipUnsupported(%d, %q)=%v want %v", c.status, c.body, got, c.want)
		}
	}
}

// TestChannelBlockedReordersPaths 渠道校验记忆只对 global 提前 /console（#296 / #308）。
//
// CN 的 /console 不提供 deepseek-v4.1-flash 等模型：一旦把 /console 排到首位，
// 该账号之后 10 分钟的**每个**请求都会先吃一次必然失败的 11102，进而把整池写成
// 模型冷却。所以 CN 保持 /v2 优先，11128 由调用方按请求级处理。
func TestChannelBlockedReordersPaths(t *testing.T) {
	c := &Client{}
	cn := &auth.Auth{UID: "u1"}
	if got := c.chatPaths(cn); got[0] != chatCompletionsPath {
		t.Fatalf("默认应 /v2 优先，got %v", got)
	}
	c.markChannelBlocked("u1")
	if got := c.chatPaths(cn); got[0] != chatCompletionsPath {
		t.Fatalf("CN 账号即使命中渠道校验也应保持 /v2 优先，got %v", got)
	}
	// global 命中后提前 /console（#296 的行为保留）
	gl := &auth.Auth{UID: "g1", Domain: "www.workbuddy.ai"}
	c.markChannelBlocked("g1")
	if got := c.chatPaths(gl); got[0] != chatConsolePath || got[1] != chatCompletionsPath {
		t.Fatalf("global 命中渠道校验后应 /console 优先，got %v", got)
	}
	// 未命中的其他账号不受影响
	if other := c.chatPaths(&auth.Auth{UID: "u2"}); other[0] != chatCompletionsPath {
		t.Fatalf("其他账号应保持 /v2 优先，got %v", other)
	}
}

// TestChannelBlockExpires TTL 到期后回落 /v2 主路径。
func TestChannelBlockExpires(t *testing.T) {
	c := &Client{}
	c.markChannelBlocked("u1")
	c.channelBlockedMu.Lock()
	c.channelBlocked["u1"] = time.Now().Add(-time.Minute) // 已过期
	c.channelBlockedMu.Unlock()
	if c.channelBlockedFor("u1") {
		t.Fatal("过期后不该再判为命中")
	}
	if got := c.chatPaths(&auth.Auth{UID: "u1"}); got[0] != chatCompletionsPath {
		t.Fatalf("过期后应回落 /v2，got %v", got)
	}
}

// TestIsChannelCheckMatchesPlain11128 issue #296 的现场报文（code 11128）。
func TestIsChannelCheckMatchesPlain11128(t *testing.T) {
	body := `{"code":11128,"msg":"request illegal","requestId":"x",` +
		`"displayTips":{"en":"This call failed channel verification, possibly because it was not sent from an official client."}}`
	if !IsChannelCheck(body) {
		t.Fatal("11128 + channel verification 必须识别为渠道校验")
	}
	if got := Classify(400, body); got != ErrAccountFault {
		t.Fatalf("11128 渠道校验应归 ErrAccountFault，got %v", got)
	}
}

// TestChannelCheckResendsOnceOnBothPaths 两条路径都被渠道校验拦时，原样重发一次
// （issue #296：上游报文自己写着「请求被拦截，请重新发送」），仍失败才返回错误。
func TestChannelCheckResendsOnceOnBothPaths(t *testing.T) {
	channel := `{"code":11128,"msg":"request illegal","displayTips":{"en":"This channel verification failed"}}`
	var calls int
	c := testClient(func(r *http.Request) (*http.Response, error) {
		calls++
		return jsonResp(400, channel), nil
	})
	a := &auth.Auth{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999}
	_, _, _, err := c.ChatStream(a, []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true}`), "", ChatMeta{})
	if err == nil {
		t.Fatal("两条路径 + 重发都被拦时应返回错误")
	}
	// /v2 + /console + 重发 1 次 = 3 次
	if calls != 3 {
		t.Fatalf("calls=%d want 3（/v2、/console、重发一次）", calls)
	}
}

// TestChannelCheckFallsBackToUncompressed 压缩请求体命中渠道校验时，先去压缩重试
// 同一条路径（issue #303：DSH 长上下文 gzip 后稳定 11128，不压缩即 200）。
func TestChannelCheckFallsBackToUncompressed(t *testing.T) {
	channel := `{"code":11128,"msg":"request illegal","displayTips":{"en":"This channel verification failed"}}`
	var gotEncoding []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enc := r.Header.Get("Content-Encoding")
		gotEncoding = append(gotEncoding, enc)
		if enc == "gzip" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(channel))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := New()
	c.GzipBody = true
	c.ChatBaseCN = srv.URL
	acct := &auth.Auth{UID: "u1", AccessToken: "at", ExpiresAt: 9999999999}
	big := `{"model":"m","messages":[{"role":"user","content":"` +
		strings.Repeat("上下文", 30000) + `"}]}`
	body, status, raw, err := c.ChatStream(acct, []byte(big), "", ChatMeta{})
	if err != nil {
		t.Fatalf("status=%d err=%v body=%s", status, err, raw)
	}
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, raw)
	}
	if body != nil {
		body.Close()
	}
	if len(gotEncoding) != 2 || gotEncoding[0] != "gzip" || gotEncoding[1] != "" {
		t.Fatalf("应当先发压缩、再去压缩重试，实际 Content-Encoding 序列=%v", gotEncoding)
	}
	if c.channelBlockedFor("u1") {
		t.Fatal("压缩重试成功的请求不该把账号标记成渠道受限")
	}
}
