package state

import "math"

// ModelQuotaWeights 内置模型倍率默认表: 厂商按该倍数扣减该模型的配额与并发额度
// (精确匹配模型名)。运行时设置 model_quota_weights 可覆盖/追加条目(见
// EffectiveModelQuotaWeights); 新厂商接入只需在设置里加键, 无需改代码。
var ModelQuotaWeights = map[string]float64{
	"glm-5.3": 2.0,
}

// SlotWeight 返回该模型单个请求占用的并发槽位单位数(半槽单位, ≥1)。
// 以客户端请求的模型名判定: 准入过滤发生在上游模型解析之前。
// 倍率 1 → 2 单位, 1.5 → 3, 2 → 4, 3 → 6(槽位记账的最小单位为半槽)。
func SlotWeight(modelName string) int64 {
	return SlotWeightIn(nil, modelName)
}

// SlotWeightIn 同 SlotWeight, 但从生效权重表取值(nil 时回退内置表)。
// 权重表来自 ConfigSnapshot.ModelQuotaWeights(编译期已合并内置与设置覆盖)。
func SlotWeightIn(weights map[string]float64, modelName string) int64 {
	if modelName == "" {
		return SlotUnitScale
	}
	if weights == nil {
		weights = ModelQuotaWeights
	}
	weight, ok := weights[modelName]
	if !ok || weight <= 0 {
		return SlotUnitScale
	}
	units := int64(math.Round(weight * float64(SlotUnitScale)))
	if units < 1 {
		return 1
	}
	return units
}

// EffectiveModelQuotaWeights 合并内置默认表与设置覆盖(同名模型以设置为准)。
// 返回新表, 不修改内置表; custom 为空时直接返回内置表。
func EffectiveModelQuotaWeights(custom map[string]float64) map[string]float64 {
	if len(custom) == 0 {
		return ModelQuotaWeights
	}
	merged := make(map[string]float64, len(custom)+len(ModelQuotaWeights))
	for modelName, weight := range ModelQuotaWeights {
		merged[modelName] = weight
	}
	for modelName, weight := range custom {
		merged[modelName] = weight
	}
	return merged
}
