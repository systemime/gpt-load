package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// 运行期检查点保存必须在启动后周期触发, 且在进程扇出退出时停止。
// 背景: 蓝绿切换用 SIGKILL, 优雅关闭的 Save 不会执行, 没有周期保存
// 每次部署都会清零冷却/黑名单/健康统计。
func TestRunCheckpointSaverSavesPeriodicallyAndStops(t *testing.T) {
	original := runtimeCheckpointInterval
	runtimeCheckpointInterval = 10 * time.Millisecond
	t.Cleanup(func() { runtimeCheckpointInterval = original })

	var saves atomic.Int64
	app := &App{runtimeCheckpoint: runtimeCheckpointFake{
		save: func(context.Context) error {
			saves.Add(1)
			return nil
		},
	}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.runCheckpointSaver(ctx)
	}()

	deadline := time.Now().Add(time.Second)
	for saves.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := saves.Load(); got < 3 {
		t.Fatalf("periodic saves = %d, want >= 3", got)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runCheckpointSaver did not stop after context cancel")
	}

	stoppedAt := saves.Load()
	time.Sleep(30 * time.Millisecond)
	if got := saves.Load(); got != stoppedAt {
		t.Fatalf("saves after stop = %d, want %d", got, stoppedAt)
	}
}
