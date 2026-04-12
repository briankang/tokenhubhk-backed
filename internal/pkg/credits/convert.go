package credits

import "math"

// CreditPerRMB 定义积分与人民币的换算比例
// 1 RMB = 10,000 credits（1元 = 1万积分）
const CreditPerRMB = 10000

// RMBToCredits 将人民币金额转换为积分
// 参数 rmb: 人民币金额（元）
// 返回: 对应的积分数量（int64）
// 换算规则: credits = round(rmb * 10000)
func RMBToCredits(rmb float64) int64 {
	return int64(math.Round(rmb * CreditPerRMB))
}

// CreditsToRMB 将积分转换为人民币金额
// 参数 credits: 积分数量
// 返回: 对应的人民币金额（元），精确到小数点后4位
// 换算规则: rmb = round(credits / 10000 * 10000) / 10000
func CreditsToRMB(credits int64) float64 {
	return math.Round(float64(credits)/CreditPerRMB*10000) / 10000
}

// CalculateWithFee 计算外币支付时的实际到账人民币金额
// 参数 amount: 原始外币金额
// 参数 rate: 汇率（外币→CNY）
// 参数 feeRate: 手续费比例（如 0.02 = 2%）
// 返回: 扣除手续费后的人民币净额
func CalculateWithFee(amount, rate, feeRate float64) float64 {
	rmbAmount := amount * rate
	fee := rmbAmount * feeRate
	return rmbAmount - fee
}

// CalculateCreditsFromForeignCurrency 计算外币支付可兑换的积分数量
// 参数 amount: 原始外币金额
// 参数 rate: 汇率（外币→CNY）
// 参数 feeRate: 手续费比例
// 返回: 可兑换的积分数量
func CalculateCreditsFromForeignCurrency(amount, rate, feeRate float64) int64 {
	rmbNet := CalculateWithFee(amount, rate, feeRate)
	return RMBToCredits(rmbNet)
}
