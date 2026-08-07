package netcfg

import (
	"sync"
	"testing"
)

// GetConfigurator 的读路径里带着写（nil 时懒初始化），而它被并发调用：
// Manager.cleanup 把 FlushAddresses / ReconcileResidualMux 等清理任务并发跑
// （runCleanupTasks），两个任务同时首次取配置器时，一个读到 nil 的同时另一个
// 正在写。无锁时 -race 稳定复现（2026-08-07 在 vopive 的 internal/device 上）。
//
// 这条用例只在 -race 下有意义；不带 -race 时它只是个冒烟测试。
func TestGetConfiguratorIsSafeUnderConcurrentFirstUse(t *testing.T) {
	original := GetConfigurator()
	t.Cleanup(func() { SetConfigurator(original) })

	// 复位成未初始化，重现"首次并发取用"这个真正出问题的时刻。
	SetConfigurator(nil)

	var wg sync.WaitGroup
	got := make([]NetworkConfigurator, 16)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got[i] = GetConfigurator()
		}(i)
	}
	wg.Wait()

	// 单例语义：所有并发调用必须拿到同一个实例。各拿各的会让
	// SetConfigurator 注入的 mock 只对一部分调用生效。
	for i, c := range got {
		if c == nil {
			t.Fatalf("第 %d 个调用拿到 nil", i)
		}
		if c != got[0] {
			t.Fatalf("第 %d 个调用拿到了不同实例 —— 单例被破坏", i)
		}
	}
}

// SetConfigurator 必须能在并发读的同时替换实例（测试注入 mock 就靠它），
// 这也是不用 sync.Once 的原因。
func TestSetConfiguratorConcurrentWithGet(t *testing.T) {
	original := GetConfigurator()
	t.Cleanup(func() { SetConfigurator(original) })

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			SetConfigurator(GetPlatformConfigurator())
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = GetConfigurator()
		}
	}()
	wg.Wait()
}
