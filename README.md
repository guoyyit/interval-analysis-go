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
   `break/continue`、带标号 `break`/`continue`）、`range`、`switch`
   （含 `fallthrough` 与带标号 `break`）、`type switch`、`select`、
   `goto`（前向与后向跳转）、提前 `return` 等结构；无法精确处理的结构按
   保守方式连接，不丢路径。
2. 在 CFG 上做工作集混沌迭代，直到状态不动点。
3. 仅在**循环回边**上、经过 `WidenAfter` 轮普通迭代后进行区间加宽
   （widen 到最近阈值或无穷），保证含循环函数有限步收敛。后向 `goto`
   构成的回边同样参与加宽，因此显式跳转写出的循环也保证终止。
4. 达到加宽不动点后执行有界次数的**收窄 / 精度提升**
   （`NarrowingPasses`）。收窄只与当前后不动点相交，只会缩小区间、
   不会增加错误的“安全”结论。

终止性由 `MaxIterations` 硬上限保证；达到上限或上下文取消时，函数标记为
未完成，绝不把未收敛结果当作完整结论。

### 控制流汇合时的赋值语义

`if/else`、`switch` 的各个 `case`（含“没有任何 `case` 命中”的隐式
路径以及 `default`）、`for`/`range` 的零次与多次执行、嵌套结构以及
`goto`/`fallthrough` 形成的所有汇合点，统一按如下规则合并状态：

- 汇合值是**所有可达前驱上可能取值的最小上界（逐点 Join）**，绝不比真实
  可能取值更窄；
- “未绑定”在语义上等于该变量声明类型的**全区间**，而不是底。因此只要
  某条前驱路径上变量未被重新绑定（例如包级变量只在一条分支里被赋窄值、
  或循环可能一次都不执行），汇合后该变量必须退回全区间：只在部分路径上
  成立的窄事实不得被带到汇合点之后；
- 数组/切片长度表按同一规则合并；
- 分支条件精化只在该分支内部生效，汇合时通过 Join 自动丢弃。

因此，汇合后的除数区间可能含 0 时必然报告 `div_by_zero`；下标区间可能
落到 `[0, len-1]` 之外时必然报告 `index_out_of_bounds`（左值写入
`a[i] = ...` 与 `a[i]++` 同样检查）；运算结果可能超出类型范围时必然
报告 `integer_overflow`。

`goto` 会在 CFG 中显式连边到标号块，普通顺序执行与跳转在同一块汇合；
分析不会因为出现跳转而跳过语句或提前结束。后向跳转标记为回边并加宽，
保证终止。

## 未完成状态的判定与退出状态约定

只有当一个函数（以及它依赖的全部函数）在预算内达到保守不动点，且收窄
阶段没有被取消时，该函数才标记为完成（`complete=true`）。以下任一情况
都会把函数显式列入 `unfinished` 并使整体 `complete=false`：

- `MaxIterations` 迭代预算耗尽仍未到不动点；
- `Timeout` 超时或 `context` 被取消；
- 递归 SCC 的摘要不动点在预算内未稳定；
- 依赖的函数未完成。

命令行在 `complete=false` 时以**非零退出码（2）**退出；结构化输出中
`unfinished` 给出未完成的函数。分析绝不会在未完成时输出“0 告警的完整
结论”：宁可显式承认未得出结论，也不给出没有可信度的安全判定。


## 保守性承诺

凡是被判定为安全的运算，在任何可达具体输入下都不会触发对应错误：

- 函数参数、外部读取、解析不了的来源一律按**全区间**处理，不做任何
  未经证明的假设；
- 外部函数 / 函数值调用的整型结果按全区间处理；
- 地址被取用、作为参数传递给调用或属于包级全局的变量，在调用点被
  保守地置为未知（havoc）；
- 加宽是上界算子，收窄只在已验证的后不动点上做；
- 配置（迭代上限、加宽阈值轮数、精度提升次数、worker 数）只允许改变
  告警数量与分析耗时：对任何**达到完成状态**的分析，调小这些参数只会
  增加误报或使函数进入未完成状态，**绝不会让任何真实可达的运行时错误
  从告警集合中消失**。预算不足时结果是 `complete=false` + 非零退出，
  而不是一份更“干净”的完整报告；
- 同一份输入在串行（`-workers 1`）与多并发（`-workers N`）下给出
  **逐条一致**的告警（位置、类别、函数、信息与 evidence 全同）：每个
  函数独立分析、共享状态只走加锁的 `SummaryStore`，告警在汇总阶段统一
  去重并按 `(文件, 偏移, 类别, 函数)` 稳定排序。

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
- 命令行以**非零退出码（2）**退出；
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

针对控制流汇合与跳转的保守性，另有以下专项验证（见 `merge_test.go`）：

- 包级整数只在 `if` 的一条分支赋值后，汇合点参与除法 → 必报
  `div_by_zero`，参与下标 → 必报 `index_out_of_bounds`；
- 包级整数只在 `switch` 的部分 `case` 中赋值（无 `default`，存在跳过
  全部 case 的路径）→ 汇合点下标运算必报越界；
- 嵌套 `if`/`switch` 汇合后取值覆盖全部路径；
- `for` 汇合包含零次执行路径，循环后下标使用必报越界；
- 前向 `goto` 跳过窄赋值形成的越界/除零路径、后向 `goto` 形成的循环
  路径上的左值下标写入均被报告，且分析有限收敛；
- 配置守恒性：在多种 `MaxIterations/WidenAfter/NarrowingPasses/Workers`
  组合下，凡收敛的分析，其告警集合保持基线告警（真实错误）不消失；
- 串行与多并发运行逐条一致（含 evidence）；
- 预算耗尽 / 取消时 `complete=false`、`unfinished` 非空，对应命令行
  非零退出，不输出零告警的完整结论。

验证方法：

```bash
go test ./...          # 全部专项与既有测试（不删减既有测试）
go test -race ./...    # 并发一致性与数据竞争
go test -run 'Merge|Goto|ConfigInvariant|SerialConcurrent' ./...
```

