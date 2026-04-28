/**
 * 折扣率格式化工具(全平台统一,2026-04-28)
 *
 * 设计原则:
 *   - 0.6 → "6.0 折"
 *   - 1.0 → "10 折 · 无折扣"
 *   - > 1.0 → "+XX% 加价"
 *   - <= 0 → "免费"
 *
 * 应用位置:
 *   - 全局折扣引擎滑块
 *   - 供应商折扣徽标
 *   - 用户特殊折扣展示
 *   - GlossaryTip 中的折扣说明
 */

/**
 * 格式化折扣率为人类可读文本
 * @param rate 折扣率(0.6 = 60% = 6 折; 1.0 = 100% = 无折扣)
 * @param compact 紧凑模式(只显示"X.X 折",不带"无折扣"等说明)
 */
export function formatDiscountRate(rate: number | null | undefined, compact = false): string {
  if (rate === null || rate === undefined || Number.isNaN(rate)) return '-'
  if (rate <= 0) return '免费'
  if (rate >= 1) {
    if (rate === 1) {
      return compact ? '10 折' : '10 折 · 无折扣'
    }
    const markupPct = (rate - 1) * 100
    return compact ? `+${markupPct.toFixed(0)}%` : `+${markupPct.toFixed(0)}% 加价`
  }
  // (0, 1) - 真折扣区
  const zhe = rate * 10
  return `${zhe.toFixed(zhe % 1 === 0 ? 0 : 1)} 折`
}

/** 折扣率的着色系统(适用于 badge) */
export function discountToneClasses(rate: number | null | undefined): { border: string; bg: string; text: string } {
  if (rate === null || rate === undefined) return { border: 'border-slate-300', bg: 'bg-slate-50', text: 'text-slate-600' }
  if (rate <= 0) return { border: 'border-purple-300', bg: 'bg-purple-50', text: 'text-purple-700' }
  if (rate < 1) {
    // 折扣越深越绿(为用户/平台越优惠)
    if (rate <= 0.5) return { border: 'border-emerald-400', bg: 'bg-emerald-50', text: 'text-emerald-700' }
    if (rate <= 0.8) return { border: 'border-cyan-300', bg: 'bg-cyan-50', text: 'text-cyan-700' }
    return { border: 'border-sky-300', bg: 'bg-sky-50', text: 'text-sky-700' }
  }
  if (rate === 1) return { border: 'border-slate-300', bg: 'bg-slate-50', text: 'text-slate-700' }
  // 加价
  return { border: 'border-amber-400', bg: 'bg-amber-50', text: 'text-amber-700' }
}

/** 是否为有效折扣(在显示区分逻辑用) */
export function hasDiscount(rate: number | null | undefined): boolean {
  if (rate === null || rate === undefined || Number.isNaN(rate)) return false
  return rate > 0 && rate < 1
}
