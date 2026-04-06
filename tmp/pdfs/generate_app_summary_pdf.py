from pathlib import Path

from reportlab.lib import colors
from reportlab.lib.enums import TA_LEFT
from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import ParagraphStyle, getSampleStyleSheet
from reportlab.lib.units import mm
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from reportlab.platypus import ListFlowable, ListItem, Paragraph, SimpleDocTemplate, Spacer


ROOT = Path(__file__).resolve().parents[2]
OUTPUT_DIR = ROOT / "output" / "pdf"
OUTPUT_PATH = OUTPUT_DIR / "gateway_app_summary_cn.pdf"


def register_fonts() -> str:
    font_candidates = [
        (r"C:\Windows\Fonts\msyh.ttc", "MicrosoftYaHei"),
        (r"C:\Windows\Fonts\simsun.ttc", "SimSun"),
        (r"C:\Windows\Fonts\simhei.ttf", "SimHei"),
    ]
    for path, name in font_candidates:
        if Path(path).exists():
            pdfmetrics.registerFont(TTFont(name, path))
            return name
    raise FileNotFoundError("No Chinese font found in Windows fonts directory.")


def make_styles(font_name: str):
    styles = getSampleStyleSheet()
    styles.add(
        ParagraphStyle(
            name="TitleCN",
            parent=styles["Title"],
            fontName=font_name,
            fontSize=17,
            leading=21,
            textColor=colors.HexColor("#16324F"),
            alignment=TA_LEFT,
            spaceAfter=4,
        )
    )
    styles.add(
        ParagraphStyle(
            name="MetaCN",
            parent=styles["Normal"],
            fontName=font_name,
            fontSize=8.5,
            leading=10.5,
            textColor=colors.HexColor("#5B6870"),
            spaceAfter=6,
        )
    )
    styles.add(
        ParagraphStyle(
            name="HeadingCN",
            parent=styles["Heading2"],
            fontName=font_name,
            fontSize=10.5,
            leading=12.5,
            textColor=colors.HexColor("#16324F"),
            spaceBefore=4,
            spaceAfter=3,
        )
    )
    styles.add(
        ParagraphStyle(
            name="BodyCN",
            parent=styles["BodyText"],
            fontName=font_name,
            fontSize=8.8,
            leading=11.2,
            textColor=colors.black,
            spaceAfter=3,
        )
    )
    styles.add(
        ParagraphStyle(
            name="BulletCN",
            parent=styles["BodyText"],
            fontName=font_name,
            fontSize=8.5,
            leading=10.6,
            leftIndent=8,
            firstLineIndent=0,
            textColor=colors.black,
            spaceAfter=1,
        )
    )
    return styles


def bullet_list(items, styles):
    return ListFlowable(
        [
            ListItem(Paragraph(item, styles["BulletCN"]), leftIndent=0)
            for item in items
        ],
        bulletType="bullet",
        start="circle",
        leftIndent=10,
        bulletFontName=styles["BulletCN"].fontName,
        bulletFontSize=7,
        spaceBefore=0,
        spaceAfter=2,
    )


def build_story(styles):
    story = [
        Paragraph("Gateway 应用一页摘要", styles["TitleCN"]),
        Paragraph(
            "基于仓库中的代码、脚本、Compose 与协议文件整理；仅在仓库未直接说明处标记为 Not found in repo.",
            styles["MetaCN"],
        ),
        Paragraph("它是什么", styles["HeadingCN"]),
        Paragraph(
            "这是一个多平面 AI Gateway：Go 编排层对外提供 HTTP API，并协调 Python 智能层与 Rust Nitro 能力层。"
            "仓库中的 README、入口代码和部署文件都表明，它试图把鉴权、路由、护栏、缓存、审计与观测收敛到统一网关中。",
            styles["BodyCN"],
        ),
        Paragraph("适合谁", styles["HeadingCN"]),
        Paragraph(
            "主要用户：Not found in repo. 按 `/v1/chat/completions`、管理接口、动态适配器、Docker Compose 与 Kubernetes 清单推断，"
            "更像面向需要统一接入和治理大模型请求的后端/平台工程师。",
            styles["BodyCN"],
        ),
        Paragraph("它能做什么", styles["HeadingCN"]),
        bullet_list(
            [
                "提供统一的 `/v1/chat/completions` 入口，支持普通响应与 SSE 流式输出。",
                "在 HTTP 路由前挂接 API Key 鉴权、Request ID、中间件式限流与配额控制。",
                "用统一 pipeline 串联请求标准化、策略评估、执行计划、同步/流式执行与审计记录。",
                "内置多种路由策略：weighted、cost、latency、quality、fallback 与 rule。",
                "通过 Python gRPC 服务执行输入/输出护栏，并用 Qdrant 做语义缓存查询。",
                "通过 Rust Nitro 做敏感信息脱敏与 Token 计数，且支持 Wasm 优先、gRPC 回退。",
                "暴露 `/metrics`、状态接口、`/dashboard` 与管理员节点/依赖/策略管理接口。",
            ],
            styles,
        ),
        Paragraph("如何工作", styles["HeadingCN"]),
        Paragraph(
            "数据流基于代码可概括为：客户端请求进入 Go 服务（Gin routes + middleware + handlers + pipeline），"
            "再按需调用 Python `AiLogic` gRPC 接口的 `CheckInput` / `CheckOutput` / `GetCache`，"
            "以及 Nitro 的 `CheckInput` / `CountTokens`；随后由 Smart Router 选择模型节点与适配器执行，"
            "结果回到 Go 层统一输出。Redis 在 Go 侧承担限流/上下文类基础设施，Qdrant 在 Python 侧承担语义缓存，"
            "Prometheus 通过 `/metrics` 抓取观测数据。",
            styles["BodyCN"],
        ),
        Paragraph("如何运行", styles["HeadingCN"]),
        bullet_list(
            [
                "根目录准备 `.env`；仓库代码至少会读取 `OPENAI_API_KEY`，并默认使用 `GATEWAY_API_KEY`/`GATEWAY_API_KEYS`。",
                "最短本地启动路径：在仓库根目录执行 `./run_all.ps1`，脚本会依次启动 Python、Rust、Go 三层服务。",
                "启动后默认入口是 `http://localhost:8080`；对外 Chat API 位于 `/v1/chat/completions`。",
                "若需要把 Redis 与 Qdrant 一并拉起，可用 `docker-compose up --build` 运行完整容器栈。",
            ],
            styles,
        ),
        Spacer(1, 2 * mm),
        Paragraph(
            "注：具体产品界面、商业定位与正式目标用户画像在仓库中未找到明确说明。",
            styles["MetaCN"],
        ),
    ]
    return story


def main():
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    font_name = register_fonts()
    styles = make_styles(font_name)

    doc = SimpleDocTemplate(
        str(OUTPUT_PATH),
        pagesize=A4,
        leftMargin=13 * mm,
        rightMargin=13 * mm,
        topMargin=11 * mm,
        bottomMargin=10 * mm,
        title="Gateway 应用一页摘要",
        author="Codex",
    )
    story = build_story(styles)
    doc.build(story)
    print(OUTPUT_PATH)


if __name__ == "__main__":
    main()
