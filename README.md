# interval-analysis-go

针对 Go 源码的**数值区间静态分析服务**。对指定目录下的包做批量分析，
在控制流图（CFG）上执行整数变量取值范围的区间抽象解释，报告三类潜在
运行时错误：

- 除数为零（`div_by_zero`）
- 数组 / 切片索引可能越界（`index_out_of_bounds`）
- 整数运算可能溢出（`integer_overflow`）

分析结合了控制流与数据流：条件分支会对区间进行精化，循环通过不动点迭代
传播，而不是对单个表达式做局部判断。

## 快速开始

```bash
# 分析单个包（目录）
go run ./cmd/analyzer -dir ./path/to/pkg

# 递归分析目录下所有包，文本输出，8 个并发 worker
go run ./cmd/analyzer -dir ./src -recursive -workers 8 -format text

# 精度/性能参数
go run ./cmd/analyzer -dir ./src \
  -max-iterations 200 \
  -widen-after 3 \
  -narrowing-passes 3 \
  -timeout 30s \
  -min-severity medium
```

也可以作为库使用：

```go
a := intervalanalysis.NewAnalyzer(intervalanalysis.DefaultConfig())
result, err := a.Analyze(context.Background(), "./src", true)
_ = intervalanalysis.WriteJSON(os.Stdout, result)
```

## 抽象域

数值抽象元素是闭区间 `[Lo, Hi]`（`int64` 表示），加上最大元 `top`，
`top` 表示对应整数类型的完整取值范围：

- 支持 `int/int8/.../uint64/uintptr/byte/rune`；
- `Join` 为逐点最小上界；
- 状态格 `State` 是“变量 → 区间”的点积映射；未绑定的整型变量默认取其
  类型全区间；
- 另维护一张“数组/切片对象 → 已知长度区间”的长度表，用于越界判定。

算术语义对加、减、乘、除、取模按区间端点给出保守结果；当任意一对端点
组合可能超出类型可表示范围时，结果上近似为全区间并产生溢出告警。
除零、`MinInt/-1` 溢出、无符号回绕均被显式建模。

分支条件（`< <= > >= == !=` 以及 `&&`、`||`、`!`）会对状态做条件
精化，条件不可满足时该后继为不可达（bottom）。

## 控制流与收敛

1. 为每个函数构建 CFG，支持 `if`、`for`（含 `init/post` 与
   `break/continue`）、`range`、`switch`、`type switch`、`select`、
   提前 `return` 等结构；无法精确处理的结构按保守方式连接，不丢路径。
2. 在 CFG 上做工作集混沌迭代，直到状态不动点。
3. 仅在**循环回边**上、经过 `WidenAfter` 轮普通迭代后进行区间加宽
   （widen 到最近阈值或无穷），保证含循环函数有限步收敛。
4. 达到加宽不动点后执行有界次数的**收窄 / 精度提升**
   （`NarrowingPasses`）。收窄只与当前后不动点相交，只会缩小区间、
   不会增加错误的“安全”结论。

终止性由 `MaxIterations` 硬上限保证；达到上限或上下文取消时，函数标记为
未完成，绝不把未收敛结果当作完整结论。

## 保守性承诺

凡是被判定为安全的运算，在任何可达具体输入下都不会触发对应错误：

- 函数参数、外部读取、解析不了的来源一律按**全区间**处理，不做任何
  未经证明的假设；
- 外部函数 / 函数值调用的整型结果按全区间处理；
- 地址被取用、作为参数传递给调用或属于包级全局的变量，在调用点被
  保守地置为未知（havoc）；
- 加宽是上界算子，收窄只在已验证的后不动点上做；
- 配置（迭代上限、加宽阈值轮数、精度提升次数、worker 数）只影响
  告警数量与耗时，**永远不会放宽安全判定**。

代价是可能存在误报（false positives），但设计目标是**不漏报**
真实错误。

## 函数摘要与增量分析

- 每个函数生成摘要：在“所有整型参数为全区间”的前提下得到的整型返回值
  区间、函数体内告警、源码哈希与完成标志。因此摘要覆盖任意具体调用，
  跨函数调用以返回值区间参与计算。
- `SummaryStore` 是并发安全的缓存，并维护 调用方 → 被调方 的反向依赖。
- 增量分析：重新运行时，源码哈希未变的函数直接复用摘要；某函数变化时，
  其全部传递依赖方通过反向索引失效并重新分析。
- 递归 SCC 作为整体做有界摘要不动点迭代，循环内调用先取全区间的
  保守初值，再迭代精化。

## 并发与一致性

- 调用图做 SCC 缩点后按依赖层级调度；同一层互不依赖的 SCC 可并行，
  worker 数量由 `Workers` 配置。
- 所有共享状态只通过加锁的 `SummaryStore` 读写。
- 告警在汇总阶段统一去重、按 `(文件, 偏移, 类别, 函数)` 稳定排序，
  因此并发结果与串行执行**逐条一致**。

## 超时与取消

支持 `context.Context` 取消与 `Timeout` 配置。分析被中断时：

- JSON/文本结果中 `complete=false`；
- `unfinished` 列出未完成的函数或包；
- 不会输出任何“未完成但安全”的结论。

## 告警语义

每条告警包含：类别、严重程度、源文件行列、所属函数、简化触发条件与
区间依据（evidence）。同一处缺陷（相同位置 + 类别 + 函数）只报告一次。

| 类别 | 默认严重程度 | 触发条件（简化） |
| --- | --- | --- |
| `div_by_zero` | high | 除数区间可能包含 0 |
| `index_out_of_bounds` | high | 索引区间可能落在 `[0, len-1]` 之外 |
| `integer_overflow` | medium | 运算可能超出该整数类型表示范围 |

可用 `-min-severity low|medium|high` 过滤，输出默认按严重程度信息与位置
稳定排序。

## 结构化输出

JSON 报告形如：

```json
{
  "complete": true,
  "unfinished": [],
  "warnings": [
    {
      "category": "div_by_zero",
      "severity": "high",
      "file": "/path/to/x.go",
      "line": 12,
      "column": 13,
      "offset": 234,
      "function": "pkg.Func",
      "message": "divisor may be zero",
      "evidence": "divisor range [-inf,+inf] may contain 0"
    }
  ]
}
```

## 测试

```bash
go test ./...
go test -race ./...
```

测试覆盖：区间算术与边界、循环收敛、条件数据流、跨函数摘要、
增量复用与依赖失效、串行/并发一致性、超时中断与严重程度过滤。
