package state

import "math"

// ModelQuotaWeights 模型倍率: 厂商按该倍数扣减该模型的配额与并发额度
// (精确匹配模型名)。token 配额折算(requestlog 聚合)与并发槽位占用共用同一系数。
var ModelQuotaWeights = map[string]float64{
	"glm-5.3": 2.0,
}

// SlotWeight 返回该模型单个请求占用的并发槽位数(≥1)。
// 以客户端请求的模型名判定: 准入过滤发生在上游模型解析之前。
func SlotWeight(modelName string) int64 {
	if modelName == "" {
		return 1
	}
	weight, ok := ModelQuotaWeights[modelName]
	if !ok || weight <= 1 {
		return 1
	}
	slots := int64(math.Ceil(weight))
	if slots < 1 {
		return 1
	}
	return slots
}
