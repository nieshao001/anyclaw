package channels

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/1024XEngineer/anyclaw/pkg/config"
)

func TestSignalFindAudioAttachmentMatchesByMIMEWithoutURL(t *testing.T) {
	adapter := &SignalAdapter{}
	attachments := []struct {
		ContentType string `json:"contentType"`
		Filename    string `json:"filename"`
	}{
		{ContentType: "audio/ogg", Filename: ""},
	}

	audioURL, audioMIME, hasAudio := adapter.findAudioAttachment(attachments)
	if !hasAudio {
		t.Fatal("expected audio attachment to be detected")
	}
	if audioMIME != "audio/ogg" {
		t.Fatalf("expected audio MIME to be preserved, got %q", audioMIME)
	}
	if audioURL != "" {
		t.Fatalf("expected empty audio URL when attachment has no filename, got %q", audioURL)
	}
}

func TestTelegramSendMessageReturnsAPIErrorWhenOKFalse(t *testing.T) {
	adapter := &TelegramAdapter{
		baseURL: "https://telegram.example/bot-token",
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if !strings.HasSuffix(req.URL.String(), "/sendMessage") {
					t.Fatalf("unexpected request URL: %s", req.URL.String())
				}
				return jsonResponse(http.StatusOK, map[string]any{"ok": false, "description": "chat not found"}), nil
			}),
		},
	}

	err := adapter.sendMessage(context.Background(), "42", "hello")
	if err == nil {
		t.Fatal("expected telegram API error")
	}
	if !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("expected chat not found error, got %v", err)
	}
}

func TestTelegramPollOncePreservesOffsetOnHandlerFailure(t *testing.T) {
	adapter := NewTelegramAdapter(config.TelegramChannelConfig{
		Enabled:   true,
		BotToken:  "token",
		PollEvery: 1,
	}, nil)

	adapter.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(req.URL.String(), "/getUpdates"):
				return jsonResponse(http.StatusOK, map[string]any{
					"ok": true,
					"result": []map[string]any{
						{
							"update_id": 7,
							"message": map[string]any{
								"text": "hello",
								"chat": map[string]any{"id": 42, "type": "private"},
								"from": map[string]any{"id": 99, "username": "alice"},
							},
						},
					},
				}), nil
			case strings.Contains(req.URL.String(), "/sendMessage"):
				return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
			default:
				return nil, fmt.Errorf("unexpected request URL: %s", req.URL.String())
			}
		}),
	}

	calls := 0
	wantErr := fmt.Errorf("boom")
	err := adapter.pollOnce(context.Background(), func(ctx context.Context, sessionID string, message string, meta map[string]string) (string, string, error) {
		calls++
		return "", "", wantErr
	})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected handler error, got %v", err)
	}
	if adapter.offset != 0 {
		t.Fatalf("expected offset to remain unchanged after failure, got %d", adapter.offset)
	}

	err = adapter.pollOnce(context.Background(), func(ctx context.Context, sessionID string, message string, meta map[string]string) (string, string, error) {
		calls++
		return "session-1", "ok", nil
	})
	if err != nil {
		t.Fatalf("expected second poll to succeed, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected failed update to be retried, got %d calls", calls)
	}
	if adapter.offset != 8 {
		t.Fatalf("expected offset to advance after success, got %d", adapter.offset)
	}
}

func TestTelegramPollOnceFallsBackToCaptionWhenTextMissing(t *testing.T) {
	adapter := NewTelegramAdapter(config.TelegramChannelConfig{
		Enabled:   true,
		BotToken:  "token",
		PollEvery: 1,
	}, nil)

	adapter.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(req.URL.String(), "/getUpdates"):
				return jsonResponse(http.StatusOK, map[string]any{
					"ok": true,
					"result": []map[string]any{
						{
							"update_id": 9,
							"message": map[string]any{
								"caption": "photo caption",
								"chat":    map[string]any{"id": 42, "type": "private"},
								"from":    map[string]any{"id": 99, "username": "alice"},
								"document": map[string]any{
									"file_id":   "doc-1",
									"mime_type": "image/png",
								},
							},
						},
					},
				}), nil
			case strings.Contains(req.URL.String(), "/sendMessage"):
				return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
			default:
				return nil, fmt.Errorf("unexpected request URL: %s", req.URL.String())
			}
		}),
	}

	calls := 0
	err := adapter.pollOnce(context.Background(), func(ctx context.Context, sessionID string, message string, meta map[string]string) (string, string, error) {
		calls++
		if message != "photo caption" {
			t.Fatalf("expected caption fallback message, got %q", message)
		}
		if meta["message_type"] != "" {
			t.Fatalf("expected plain text caption flow, got %+v", meta)
		}
		return "session-1", "ok", nil
	})
	if err != nil {
		t.Fatalf("pollOnce failed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected caption-only update to reach handler once, got %d", calls)
	}
	if adapter.offset != 10 {
		t.Fatalf("expected offset to advance after caption fallback, got %d", adapter.offset)
	}
}

func TestTelegramPollOnceProcessesVoiceMessagesWithoutTokenLeak(t *testing.T) {
	var (
		outbound      []string
		eventType     string
		eventSession  string
		eventPayload  map[string]any
		getFileCalls  int32
		downloadCalls int32
		audioPath     string
	)

	adapter := NewTelegramAdapter(config.TelegramChannelConfig{
		Enabled:   true,
		BotToken:  "secret-token",
		PollEvery: 1,
	}, func(kind string, sessionID string, payload map[string]any) {
		eventType = kind
		eventSession = sessionID
		eventPayload = payload
	})

	adapter.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(req.URL.String(), "/getUpdates"):
				return jsonResponse(http.StatusOK, map[string]any{
					"ok": true,
					"result": []map[string]any{
						{
							"update_id": 8,
							"message": map[string]any{
								"caption": "voice caption",
								"chat":    map[string]any{"id": 42, "type": "private"},
								"from":    map[string]any{"id": 99, "username": "alice"},
								"voice":   map[string]any{"file_id": "voice-1", "mime_type": "audio/ogg"},
							},
						},
					},
				}), nil
			case strings.Contains(req.URL.String(), "/getFile"):
				atomic.AddInt32(&getFileCalls, 1)
				return jsonResponse(http.StatusOK, map[string]any{
					"ok":     true,
					"result": map[string]any{"file_id": "voice-1", "file_path": "voice/path.ogg"},
				}), nil
			case strings.Contains(req.URL.String(), "/file/botsecret-token/voice/path.ogg"):
				atomic.AddInt32(&downloadCalls, 1)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("voice-bytes")),
					Header:     make(http.Header),
				}, nil
			case strings.Contains(req.URL.String(), "/sendMessage"):
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read request body: %v", err)
				}
				outbound = append(outbound, string(body))
				return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
			default:
				return nil, fmt.Errorf("unexpected request URL: %s", req.URL.String())
			}
		}),
	}

	err := adapter.pollOnce(context.Background(), func(ctx context.Context, sessionID string, message string, meta map[string]string) (string, string, error) {
		if meta["message_type"] != "voice_note" {
			t.Fatalf("expected voice_note meta, got %+v", meta)
		}
		if got := meta["audio_ref"]; got != "telegram-file:voice-1" {
			t.Fatalf("expected opaque audio ref, got %+v", meta)
		}
		if got := meta["audio_file_id"]; got != "voice-1" {
			t.Fatalf("expected audio file id, got %+v", meta)
		}
		if got := meta["audio_mime"]; got != "audio/ogg" {
			t.Fatalf("expected audio mime, got %+v", meta)
		}
		if got := meta["audio_url"]; got == "" {
			t.Fatalf("expected retrievable audio path, got %+v", meta)
		} else {
			audioPath = got
			if strings.Contains(got, "secret-token") {
				t.Fatalf("expected audio path to hide bot token, got %+v", meta)
			}
			data, err := os.ReadFile(got)
			if err != nil {
				t.Fatalf("expected audio path to be readable: %v", err)
			}
			if string(data) != "voice-bytes" {
				t.Fatalf("expected downloaded audio bytes, got %q", string(data))
			}
		}
		if strings.Contains(meta["audio_ref"], "secret-token") {
			t.Fatalf("expected audio ref to hide bot token, got %+v", meta)
		}
		if message != meta["audio_url"] {
			t.Fatalf("expected message payload to use audio path, got %q meta=%+v", message, meta)
		}
		return "session-voice", "voice reply", nil
	})
	if err != nil {
		t.Fatalf("pollOnce failed: %v", err)
	}
	if len(outbound) != 1 || !strings.Contains(outbound[0], "text=voice+reply") {
		t.Fatalf("expected voice reply to be sent once, got %+v", outbound)
	}
	if adapter.offset != 9 {
		t.Fatalf("expected offset to advance after voice success, got %d", adapter.offset)
	}
	if atomic.LoadInt32(&getFileCalls) != 1 {
		t.Fatalf("expected one getFile call, got %d", getFileCalls)
	}
	if atomic.LoadInt32(&downloadCalls) != 1 {
		t.Fatalf("expected one file download call, got %d", downloadCalls)
	}
	if eventType != "channel.telegram.voice" || eventSession != "session-voice" {
		t.Fatalf("unexpected event info: %q %q", eventType, eventSession)
	}
	if got := eventPayload["audio_ref"]; got != "telegram-file:voice-1" {
		t.Fatalf("expected opaque event audio ref, got %+v", eventPayload)
	}
	if got := eventPayload["audio_file_id"]; got != "voice-1" {
		t.Fatalf("expected event audio file id, got %+v", eventPayload)
	}
	if got := eventPayload["audio_mime"]; got != "audio/ogg" {
		t.Fatalf("expected event audio mime, got %+v", eventPayload)
	}
	if _, ok := eventPayload["audio_url"]; ok {
		t.Fatalf("expected event payload to omit audio_url, got %+v", eventPayload)
	}
	if audioPath == "" {
		t.Fatal("expected audio path to be captured during handler execution")
	}
	if _, err := os.Stat(audioPath); !os.IsNotExist(err) {
		t.Fatalf("expected temporary audio file to be cleaned up, stat err=%v", err)
	}
}

func TestTelegramPollOnceStreamProcessesVoiceMessages(t *testing.T) {
	var (
		outbound      []string
		eventType     string
		eventSession  string
		eventPayload  map[string]any
		getFileCalls  int32
		downloadCalls int32
		audioPath     string
	)

	adapter := NewTelegramAdapter(config.TelegramChannelConfig{
		Enabled:   true,
		BotToken:  "secret-token",
		PollEvery: 1,
	}, func(kind string, sessionID string, payload map[string]any) {
		eventType = kind
		eventSession = sessionID
		eventPayload = payload
	})

	adapter.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(req.URL.String(), "/getUpdates"):
				return jsonResponse(http.StatusOK, map[string]any{
					"ok": true,
					"result": []map[string]any{
						{
							"update_id": 8,
							"message": map[string]any{
								"caption": "voice caption",
								"chat":    map[string]any{"id": 42, "type": "private"},
								"from":    map[string]any{"id": 99, "username": "alice"},
								"voice":   map[string]any{"file_id": "voice-1", "mime_type": "audio/ogg"},
							},
						},
					},
				}), nil
			case strings.Contains(req.URL.String(), "/getFile"):
				atomic.AddInt32(&getFileCalls, 1)
				return jsonResponse(http.StatusOK, map[string]any{
					"ok":     true,
					"result": map[string]any{"file_id": "voice-1", "file_path": "voice/path.ogg"},
				}), nil
			case strings.Contains(req.URL.String(), "/file/botsecret-token/voice/path.ogg"):
				atomic.AddInt32(&downloadCalls, 1)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("voice-bytes")),
					Header:     make(http.Header),
				}, nil
			case strings.Contains(req.URL.String(), "/sendMessage"):
				body, err := io.ReadAll(req.Body)
				if err != nil {
					t.Fatalf("read request body: %v", err)
				}
				outbound = append(outbound, string(body))
				return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
			default:
				return nil, fmt.Errorf("unexpected request URL: %s", req.URL.String())
			}
		}),
	}

	err := adapter.pollOnceStream(context.Background(), func(ctx context.Context, sessionID string, message string, meta map[string]string, onChunk func(chunk string) error) (string, error) {
		if meta["message_type"] != "voice_note" {
			t.Fatalf("expected voice_note meta, got %+v", meta)
		}
		if got := meta["audio_ref"]; got != "telegram-file:voice-1" {
			t.Fatalf("expected opaque audio ref in meta, got %+v", meta)
		}
		if got := meta["audio_file_id"]; got != "voice-1" {
			t.Fatalf("expected audio file id in meta, got %+v", meta)
		}
		if got := meta["audio_mime"]; got != "audio/ogg" {
			t.Fatalf("expected audio mime in meta, got %+v", meta)
		}
		if got := meta["audio_url"]; got == "" {
			t.Fatalf("expected retrievable audio path in meta, got %+v", meta)
		} else {
			audioPath = got
			if strings.Contains(got, "secret-token") {
				t.Fatalf("expected audio path to hide bot token, got %+v", meta)
			}
			data, err := os.ReadFile(got)
			if err != nil {
				t.Fatalf("expected audio path to be readable: %v", err)
			}
			if string(data) != "voice-bytes" {
				t.Fatalf("expected downloaded audio bytes, got %q", string(data))
			}
		}
		if strings.Contains(meta["audio_ref"], "secret-token") {
			t.Fatalf("expected audio ref to hide bot token, got %+v", meta)
		}
		if meta["caption"] != "voice caption" {
			t.Fatalf("expected caption to be preserved, got %+v", meta)
		}
		if message != meta["audio_url"] {
			t.Fatalf("expected streaming payload to use audio path, got %q meta=%+v", message, meta)
		}
		if err := onChunk("voice reply"); err != nil {
			return "", err
		}
		return "session-1", nil
	})
	if err != nil {
		t.Fatalf("pollOnceStream failed: %v", err)
	}
	if len(outbound) != 1 || !strings.Contains(outbound[0], "text=voice+reply") {
		t.Fatalf("expected voice reply to be sent once, got %+v", outbound)
	}
	if adapter.offset != 9 {
		t.Fatalf("expected offset to advance after streaming voice success, got %d", adapter.offset)
	}
	if atomic.LoadInt32(&getFileCalls) != 1 {
		t.Fatalf("expected one getFile call, got %d", getFileCalls)
	}
	if atomic.LoadInt32(&downloadCalls) != 1 {
		t.Fatalf("expected one file download call, got %d", downloadCalls)
	}
	if eventType != "channel.telegram.voice" || eventSession != "session-1" {
		t.Fatalf("unexpected event info: %q %q", eventType, eventSession)
	}
	if got := eventPayload["audio_ref"]; got != "telegram-file:voice-1" {
		t.Fatalf("expected opaque event audio ref, got %+v", eventPayload)
	}
	if got := eventPayload["audio_file_id"]; got != "voice-1" {
		t.Fatalf("expected event audio file id, got %+v", eventPayload)
	}
	if got := eventPayload["audio_mime"]; got != "audio/ogg" {
		t.Fatalf("expected event audio mime, got %+v", eventPayload)
	}
	if _, ok := eventPayload["audio_url"]; ok {
		t.Fatalf("expected event payload to omit audio_url, got %+v", eventPayload)
	}
	if got := eventPayload["streaming"]; got != true {
		t.Fatalf("expected streaming voice event, got %+v", eventPayload)
	}
	if audioPath == "" {
		t.Fatal("expected audio path to be captured during handler execution")
	}
	if _, err := os.Stat(audioPath); !os.IsNotExist(err) {
		t.Fatalf("expected temporary audio file to be cleaned up, stat err=%v", err)
	}
}

func TestTelegramSendStreamingMessageDeletesPlaceholderWhenNoChunksArrive(t *testing.T) {
	var (
		sendCalls   int32
		deleteCalls int32
		editCalls   int32
	)

	adapter := NewTelegramAdapter(config.TelegramChannelConfig{
		Enabled:   true,
		BotToken:  "token",
		PollEvery: 1,
	}, nil)

	adapter.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(req.URL.String(), "/sendMessage"):
				atomic.AddInt32(&sendCalls, 1)
				return jsonResponse(http.StatusOK, map[string]any{
					"ok": true,
					"result": map[string]any{
						"message_id": 12,
					},
				}), nil
			case strings.Contains(req.URL.String(), "/deleteMessage"):
				atomic.AddInt32(&deleteCalls, 1)
				return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
			case strings.Contains(req.URL.String(), "/editMessageText"):
				atomic.AddInt32(&editCalls, 1)
				return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
			default:
				return nil, fmt.Errorf("unexpected request URL: %s", req.URL.String())
			}
		}),
	}

	if err := adapter.sendStreamingMessage(context.Background(), "42", func(onChunk func(chunk string) error) error {
		return nil
	}); err != nil {
		t.Fatalf("sendStreamingMessage failed: %v", err)
	}
	if atomic.LoadInt32(&sendCalls) != 1 {
		t.Fatalf("expected placeholder message to be sent once, got %d", sendCalls)
	}
	if atomic.LoadInt32(&deleteCalls) != 1 {
		t.Fatalf("expected placeholder message to be deleted once, got %d", deleteCalls)
	}
	if atomic.LoadInt32(&editCalls) != 0 {
		t.Fatalf("expected no empty edit request, got %d", editCalls)
	}
}

func TestTelegramSendStreamingMessageDeletesPlaceholderWhenStreamFailsWithoutChunks(t *testing.T) {
	var (
		sendCalls   int32
		deleteCalls int32
		editCalls   int32
	)

	adapter := NewTelegramAdapter(config.TelegramChannelConfig{
		Enabled:   true,
		BotToken:  "token",
		PollEvery: 1,
	}, nil)

	adapter.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(req.URL.String(), "/sendMessage"):
				atomic.AddInt32(&sendCalls, 1)
				return jsonResponse(http.StatusOK, map[string]any{
					"ok": true,
					"result": map[string]any{
						"message_id": 15,
					},
				}), nil
			case strings.Contains(req.URL.String(), "/deleteMessage"):
				atomic.AddInt32(&deleteCalls, 1)
				return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
			case strings.Contains(req.URL.String(), "/editMessageText"):
				atomic.AddInt32(&editCalls, 1)
				return jsonResponse(http.StatusOK, map[string]any{"ok": true}), nil
			default:
				return nil, fmt.Errorf("unexpected request URL: %s", req.URL.String())
			}
		}),
	}

	wantErr := fmt.Errorf("stream failed")
	err := adapter.sendStreamingMessage(context.Background(), "42", func(onChunk func(chunk string) error) error {
		return wantErr
	})
	if err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
		t.Fatalf("expected original stream error, got %v", err)
	}
	if atomic.LoadInt32(&sendCalls) != 1 {
		t.Fatalf("expected placeholder message to be sent once, got %d", sendCalls)
	}
	if atomic.LoadInt32(&deleteCalls) != 1 {
		t.Fatalf("expected placeholder message to be deleted once, got %d", deleteCalls)
	}
	if atomic.LoadInt32(&editCalls) != 0 {
		t.Fatalf("expected no empty edit request, got %d", editCalls)
	}
}

func TestSignalPollOnceRetriesMessageUntilSendSucceeds(t *testing.T) {
	sendCalls := 0
	handleCalls := 0

	adapter := NewSignalAdapter(config.SignalChannelConfig{
		Enabled:   true,
		BaseURL:   "https://signal.example",
		Number:    "+1000",
		PollEvery: 1,
	}, nil)

	adapter.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch {
			case strings.Contains(req.URL.String(), "/v1/receive/"):
				return jsonResponse(http.StatusOK, []map[string]any{
					{
						"envelope": map[string]any{
							"timestamp":  123,
							"source":     "+2000",
							"sourceName": "bob",
							"dataMessage": map[string]any{
								"message": "hello",
							},
						},
					},
				}), nil
			case strings.Contains(req.URL.String(), "/v2/send"):
				sendCalls++
				if sendCalls == 1 {
					return jsonResponse(http.StatusBadGateway, map[string]any{"error": "fail"}), nil
				}
				return jsonResponse(http.StatusOK, map[string]any{"timestamp": 124}), nil
			default:
				return nil, fmt.Errorf("unexpected request URL: %s", req.URL.String())
			}
		}),
	}

	err := adapter.pollOnce(context.Background(), func(ctx context.Context, sessionID string, message string, meta map[string]string) (string, string, error) {
		handleCalls++
		return "session-1", "ok", nil
	})
	if err == nil {
		t.Fatal("expected first send failure")
	}
	if adapter.latestTS != 0 {
		t.Fatalf("expected latestTS to remain unchanged after send failure, got %d", adapter.latestTS)
	}

	err = adapter.pollOnce(context.Background(), func(ctx context.Context, sessionID string, message string, meta map[string]string) (string, string, error) {
		handleCalls++
		return "session-1", "ok", nil
	})
	if err != nil {
		t.Fatalf("expected second poll to succeed, got %v", err)
	}
	if handleCalls != 2 {
		t.Fatalf("expected message to be retried, got %d handler calls", handleCalls)
	}
	if adapter.latestTS != 123 {
		t.Fatalf("expected latestTS to advance after success, got %d", adapter.latestTS)
	}
}

func TestDiscordPollOnceRepliesToChannelInsteadOfParentMessageID(t *testing.T) {
	var postedURL string
	var postedBody string
	adapter := &DiscordAdapter{
		config: config.DiscordChannelConfig{
			DefaultChannel: "c1",
		},
		apiBaseURL: "https://discord.example/api/v10",
		client: &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.Method {
				case http.MethodGet:
					return jsonResponse(http.StatusOK, []map[string]any{
						{
							"id":         "m1",
							"channel_id": "c1",
							"content":    "hello",
							"guild_id":   "g1",
							"author":     map[string]any{"id": "u1", "username": "alice", "bot": false},
							"message_reference": map[string]any{
								"message_id": "parent-123",
								"channel_id": "c1",
							},
						},
					}), nil
				case http.MethodPost:
					body, err := io.ReadAll(req.Body)
					if err != nil {
						t.Fatalf("read request body: %v", err)
					}
					postedURL = req.URL.String()
					postedBody = string(body)
					return jsonResponse(http.StatusOK, map[string]any{"id": "reply-1"}), nil
				default:
					t.Fatalf("unexpected method: %s", req.Method)
					return nil, nil
				}
			}),
		},
		processed: make(map[string]time.Time),
	}

	err := adapter.pollOnce(context.Background(), func(ctx context.Context, sessionID string, message string, meta map[string]string) (string, string, error) {
		return "", "reply", nil
	})
	if err != nil {
		t.Fatalf("pollOnce failed: %v", err)
	}
	if !strings.Contains(postedURL, "/channels/c1/messages") {
		t.Fatalf("expected reply to post to channel c1, got %s", postedURL)
	}
	if !strings.Contains(postedBody, `"message_reference":{"message_id":"parent-123"}`) {
		t.Fatalf("expected reply to preserve parent message reference, got %s", postedBody)
	}
}
