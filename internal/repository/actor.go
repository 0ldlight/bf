package repository

// Actor 是可空的操作者纯值（目标 createdBy/modifiedBy）：封装的原始
// UTF-16 码元序列与 null 状态。私有存储只有一份码元切片：nil 表示
// null，非 nil（可为空）表示 present；零 Actor 即 null。无 setter。
// Actor is the nullable actor pure value (the target's createdBy/modifiedBy):
// an encapsulated raw UTF-16 unit sequence plus nullness. The private
// storage is a single unit slice: nil means null, nonnil (possibly empty)
// means present; the zero Actor is null. There are no setters.
type Actor struct {
	units []uint16
}

const (
	// actorMaxUnits 对齐目标 Node 构造器对 createdBy/modifiedBy 的
	// StringUtils.abbreviate(value,64) 上界：64 个 UTF-16 码元。
	// actorMaxUnits mirrors the target Node constructor's
	// StringUtils.abbreviate(value,64) bound for createdBy/modifiedBy:
	// 64 UTF-16 units.
	actorMaxUnits = 64
	// actorKeepUnits 是超过上界时保留的原始码元数：首 61 个。
	// actorKeepUnits is the raw-unit count kept above the bound: the
	// first 61.
	actorKeepUnits = 61
	// actorDot 是缩写后缀使用的 ASCII 句点码元 0x002e，共三个。
	// actorDot is the ASCII full-stop unit 0x002e used three times for
	// the abbreviation suffix.
	actorDot = 0x002e
)

// NewActor 构造 Actor：nil 输入表示 Java null；非 nil 空切片表示 Java
// 空串。长度 <=64 原样保留；>64 只保留首 61 个码元再加三个 0x002e。接受
// 每个 uint16（NUL、未配对/有效代理对），不做 trim、归一化、UTF-8 解码、
// 权限判断或拒绝，也不返回错误。输入后备存储从不保留：构造即复制，保留
// 数据无论输入多大都不超过 64 个码元。
// NewActor constructs an Actor: nil input means the Java null; a nonnil
// empty slice means the Java empty string. Lengths <=64 are kept verbatim;
// above 64 only the first 61 units plus three 0x002e units survive. Every
// uint16 is accepted (NUL, unpaired and valid surrogate pairs) with no
// trim, normalization, UTF-8 decoding, authorization or rejection, and no
// error result. The input backing store is never retained: construction
// copies, and the retained data never exceeds 64 units whatever the input
// size.
func NewActor(raw []uint16) Actor {
	if raw == nil {
		return Actor{}
	}
	var units []uint16
	if len(raw) <= actorMaxUnits {
		units = make([]uint16, len(raw))
		copy(units, raw)
	} else {
		units = make([]uint16, 0, actorMaxUnits)
		units = append(units, raw[:actorKeepUnits]...)
		units = append(units, actorDot, actorDot, actorDot)
	}
	return Actor{units: units}
}

// IsNull 报告 Actor 是否为 null；零 Actor 与 NewActor(nil) 都是 null。
// IsNull reports whether the Actor is null; the zero Actor and
// NewActor(nil) are both null.
func (a Actor) IsNull() bool { return a.units == nil }

// UTF16 返回码元的防御性副本：null 返回 nil，present-empty 返回非 nil
// 零长度。修改构造输入或任何返回切片都不会影响既有 Actor 或其值副本。
// 有意不提供自定义 String/UTF-8/JSON/driver.Valuer/Scanner 转换：'?'只是
// 所选 PostgreSQL 参数编码边界上的表现，本领域值保留原始未配对码元；默认
// fmt/反射行为不是脱敏或传输保证。
// UTF16 returns a defensive copy of the units: nil for null, a nonnil zero
// length for present-empty. Mutating the construction input or any returned
// slice can never affect an existing Actor or its value copies. No custom
// String/UTF-8/JSON/driver.Valuer/Scanner conversion is provided on purpose:
// '?' is only the appearance at the selected PostgreSQL parameter-encoding
// boundary while this domain value keeps the raw unpaired unit; default
// fmt/reflection behavior is not a redaction or transport guarantee.
func (a Actor) UTF16() []uint16 {
	if a.units == nil {
		return nil
	}
	units := make([]uint16, len(a.units))
	copy(units, a.units)
	return units
}
