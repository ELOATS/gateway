package router

import (
	"testing"
	"time"
)

// --- 辅助函数 ---

func makeNodes() []*ModelNode {
	return []*ModelNode{
		{Name: "cheap", ModelID: "m1", Weight: 20, CostPer1K: 0.001, Quality: 0.5, Enabled: true, Tags: map[string]string{"tier": "economy"}},
		{Name: "balanced", ModelID: "m2", Weight: 60, CostPer1K: 0.01, Quality: 0.8, Enabled: true, Tags: map[string]string{"tier": "standard"}},
		{Name: "premium", ModelID: "m3", Weight: 20, CostPer1K: 0.03, Quality: 0.95, Enabled: true, Tags: map[string]string{"tier": "premium"}},
	}
}

func defaultCtx() *RouteContext {
	return &RouteContext{RequestID: "test-1", Model: "gpt-4", UserTier: "default"}
}

// --- 策略测试 ---

func TestWeightedStrategy_ReturnsNode(t *testing.T) {
	s := &WeightedStrategy{}
	nodes := makeNodes()

	result := s.Select(defaultCtx(), nodes)
	if result == nil {
		t.Fatal("WeightedStrategy 返回了 nil")
	}
}

func TestWeightedStrategy_EmptyNodes(t *testing.T) {
	s := &WeightedStrategy{}
	result := s.Select(defaultCtx(), []*ModelNode{})
	if result != nil {
		t.Fatal("空节点列表应返回 nil")
	}
}

func TestCostStrategy_SelectsCheapest(t *testing.T) {
	s := &CostStrategy{MinQuality: 0.0}
	nodes := makeNodes()

	result := s.Select(defaultCtx(), nodes)
	if result == nil || result.Name != "cheap" {
		t.Fatalf("CostStrategy 应选择最便宜的节点 'cheap'，实际选择了 %v", result)
	}
}

func TestCostStrategy_RespectsMinQuality(t *testing.T) {
	s := &CostStrategy{MinQuality: 0.7}
	nodes := makeNodes()

	result := s.Select(defaultCtx(), nodes)
	if result == nil || result.Name != "balanced" {
		t.Fatalf("CostStrategy 应选择满足 MinQuality=0.7 中最便宜的 'balanced'，实际选择了 %v", result)
	}
}

func TestCostStrategy_FallbackToHighestQuality(t *testing.T) {
	s := &CostStrategy{MinQuality: 0.99}
	nodes := makeNodes()

	result := s.Select(defaultCtx(), nodes)
	// 没有节点满足 0.99 质量要求，应退化到质量最高的节点。
	if result == nil || result.Name != "premium" {
		t.Fatalf("CostStrategy 应退化选择质量最高的 'premium'，实际选择了 %v", result)
	}
}

func TestQualityStrategy_SelectsHighestQuality(t *testing.T) {
	tracker := NewHealthTracker(0.3)
	s := &QualityStrategy{Tracker: tracker}
	nodes := makeNodes()

	result := s.Select(defaultCtx(), nodes)
	if result == nil || result.Name != "premium" {
		t.Fatalf("QualityStrategy 应选择质量最高的 'premium'，实际选择了 %v", result)
	}
}

func TestLatencyStrategy_PrefersLowLatency(t *testing.T) {
	tracker := NewHealthTracker(0.3)
	// 模拟延迟数据。
	tracker.RecordSuccess("cheap", 500*time.Millisecond)
	tracker.RecordSuccess("balanced", 100*time.Millisecond)
	tracker.RecordSuccess("premium", 300*time.Millisecond)

	s := &LatencyStrategy{Tracker: tracker}
	nodes := makeNodes()

	result := s.Select(defaultCtx(), nodes)
	if result == nil || result.Name != "balanced" {
		t.Fatalf("LatencyStrategy 应选择延迟最低的 'balanced'，实际选择了 %v", result)
	}
}

func TestLatencyStrategy_ExploresNewNodes(t *testing.T) {
	tracker := NewHealthTracker(0.3)
	// 只有一个节点有延迟记录，另两个从未被调用。
	tracker.RecordSuccess("balanced", 100*time.Millisecond)

	s := &LatencyStrategy{Tracker: tracker}
	nodes := makeNodes()

	result := s.Select(defaultCtx(), nodes)
	// 从未被调用的节点应获得极低的虚拟延迟，从而被优先尝试。
	if result == nil || result.Name == "balanced" {
		t.Fatalf("LatencyStrategy 应优先探索从未调用的节点，实际选了 %v", result)
	}
}

func TestFallbackStrategy_SkipsUnhealthy(t *testing.T) {
	tracker := NewHealthTracker(0.3)
	// 模拟第一个节点故障。
	for i := 0; i < 10; i++ {
		tracker.RecordFailure("cheap")
	}
	tracker.RecordSuccess("balanced", 100*time.Millisecond)

	s := &FallbackStrategy{Tracker: tracker}
	nodes := makeNodes()

	result := s.Select(defaultCtx(), nodes)
	if result == nil || result.Name != "balanced" {
		t.Fatalf("FallbackStrategy 应跳过不健康的 'cheap'，选择 'balanced'，实际选择了 %v", result)
	}
}

func TestRuleStrategy_MatchesRule(t *testing.T) {
	rules := []Rule{
		{
			Name: "VIP 路由", Priority: 1, Target: "premium",
			Condition: func(ctx *RouteContext) bool { return ctx.UserTier == "vip" },
		},
	}
	s := NewRuleStrategy(rules)
	nodes := makeNodes()
	ctx := &RouteContext{RequestID: "test", UserTier: "vip"}

	result := s.Select(ctx, nodes)
	if result == nil || result.Name != "premium" {
		t.Fatalf("RuleStrategy 应命中 VIP 规则并路由到 'premium'，实际选择了 %v", result)
	}
}

func TestRuleStrategy_NoMatchReturnsNil(t *testing.T) {
	rules := []Rule{
		{
			Name: "VIP 路由", Priority: 1, Target: "premium",
			Condition: func(ctx *RouteContext) bool { return ctx.UserTier == "vip" },
		},
	}
	s := NewRuleStrategy(rules)
	nodes := makeNodes()
	ctx := &RouteContext{RequestID: "test", UserTier: "free"}

	result := s.Select(ctx, nodes)
	if result != nil {
		t.Fatalf("RuleStrategy 无匹配规则时应返回 nil，实际返回了 %v", result)
	}
}

// --- 健康追踪器测试 ---

func TestHealthTracker_EWMA(t *testing.T) {
	ht := NewHealthTracker(0.5)

	ht.RecordSuccess("node1", 100*time.Millisecond)
	h1 := ht.GetHealth("node1")
	if h1.AvgLatency != 0.1 {
		t.Fatalf("首次 EWMA 应为 0.1，实际为 %f", h1.AvgLatency)
	}

	ht.RecordSuccess("node1", 200*time.Millisecond)
	h2 := ht.GetHealth("node1")
	// EWMA: 0.5 * 0.2 + 0.5 * 0.1 = 0.15
	expected := 0.15
	if h2.AvgLatency < expected-0.01 || h2.AvgLatency > expected+0.01 {
		t.Fatalf("第二次 EWMA 应接近 0.15，实际为 %f", h2.AvgLatency)
	}
}

func TestHealthTracker_HealthyByDefault(t *testing.T) {
	ht := NewHealthTracker(0.3)
	if !ht.IsHealthy("unknown") {
		t.Fatal("从未追踪的节点应被视为健康")
	}
}

func TestHealthTracker_UnhealthyOnHighErrorRate(t *testing.T) {
	ht := NewHealthTracker(0.3)
	// 模拟 6 次失败 / 4 次成功 → 60% 错误率。
	for i := 0; i < 6; i++ {
		ht.RecordFailure("bad-node")
	}
	for i := 0; i < 4; i++ {
		ht.RecordSuccess("bad-node", 100*time.Millisecond)
	}

	if ht.IsHealthy("bad-node") {
		t.Fatal("60% 错误率的节点应被视为不健康")
	}
}

// --- 核心路由器测试 ---

func TestSmartRouter_Route_Default(t *testing.T) {
	nodes := makeNodes()
	tracker := NewHealthTracker(0.3)
	sr := NewSmartRouter(nodes, tracker, "weighted")
	sr.RegisterStrategy(&WeightedStrategy{})

	result, err := sr.Route(defaultCtx())
	if err != nil {
		t.Fatalf("Route 返回了错误: %v", err)
	}
	if result == nil {
		t.Fatal("Route 返回了 nil")
	}
}

func TestSmartRouter_Route_HeaderOverride(t *testing.T) {
	nodes := makeNodes()
	tracker := NewHealthTracker(0.3)
	sr := NewSmartRouter(nodes, tracker, "weighted")
	sr.RegisterStrategy(&WeightedStrategy{})
	sr.RegisterStrategy(&QualityStrategy{Tracker: tracker})

	ctx := &RouteContext{
		RequestID: "test",
		Model:     "gpt-4",
		Headers:   map[string]string{"X-Route-Strategy": "quality"},
	}

	result, err := sr.Route(ctx)
	if err != nil {
		t.Fatalf("Route 返回了错误: %v", err)
	}
	if result == nil || result.Name != "premium" {
		t.Fatalf("Header 指定 quality 策略应路由到 'premium'，实际选择了 %v", result)
	}
}

func TestSmartRouter_FiltersDisabledNodes(t *testing.T) {
	nodes := makeNodes()
	nodes[0].Enabled = false // 禁用 cheap。
	nodes[1].Enabled = false // 禁用 balanced。

	tracker := NewHealthTracker(0.3)
	sr := NewSmartRouter(nodes, tracker, "quality")
	sr.RegisterStrategy(&QualityStrategy{Tracker: tracker})

	result, err := sr.Route(defaultCtx())
	if err != nil {
		t.Fatalf("Route 返回了错误: %v", err)
	}
	if result == nil || result.Name != "premium" {
		t.Fatalf("应仅返回启用的 'premium' 节点，实际选择了 %v", result)
	}
}

func TestSmartRouter_AllDisabledReturnsError(t *testing.T) {
	nodes := makeNodes()
	for _, n := range nodes {
		n.Enabled = false
	}

	tracker := NewHealthTracker(0.3)
	sr := NewSmartRouter(nodes, tracker, "weighted")
	sr.RegisterStrategy(&WeightedStrategy{})

	_, err := sr.Route(defaultCtx())
	if err == nil {
		t.Fatal("所有节点禁用时 Route 应返回错误")
	}
}

// --- 基准测试 (Benchmarks) ---

func BenchmarkWeightedStrategy_Select_10Nodes(b *testing.B) {
	s := &WeightedStrategy{}
	nodes := make([]*ModelNode, 10)
	for i := 0; i < 10; i++ {
		nodes[i] = &ModelNode{Name: "n", Weight: 10, Enabled: true}
	}
	ctx := defaultCtx()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Select(ctx, nodes)
	}
}

func BenchmarkWeightedStrategy_Select_100Nodes(b *testing.B) {
	s := &WeightedStrategy{}
	nodes := make([]*ModelNode, 100)
	for i := 0; i < 100; i++ {
		nodes[i] = &ModelNode{Name: "n", Weight: 10, Enabled: true}
	}
	ctx := defaultCtx()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Select(ctx, nodes)
	}
}

func BenchmarkSmartRouter_Route_10Nodes(b *testing.B) {
	nodes := make([]*ModelNode, 10)
	for i := 0; i < 10; i++ {
		nodes[i] = &ModelNode{Name: "n", Weight: 10, Enabled: true}
	}
	tracker := NewHealthTracker(0.3)
	sr := NewSmartRouter(nodes, tracker, "weighted")
	sr.RegisterStrategy(&WeightedStrategy{})
	ctx := defaultCtx()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sr.Route(ctx)
	}
}
