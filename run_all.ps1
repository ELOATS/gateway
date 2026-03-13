# AI Gateway 一键启动脚本 (Windows PowerShell)

# 1. 设置环境变量
$env:GATEWAY_API_KEY = "sk-gw-default-123456"
$env:OPENAI_API_KEY = "您的_OPENAI_KEY"

Write-Host "--- 正在清理旧进程以释放文件锁 ---" -ForegroundColor Cyan
$processes = @("gateway", "python", "cargo", "utils-rust")
foreach ($p in $processes) {
    Stop-Process -Name $p -ErrorAction SilentlyContinue
}
Start-Sleep -Seconds 1

Write-Host "--- 正在启动系统各平面 ---" -ForegroundColor Cyan

# 2. 启动 Python 智能层
Write-Host "[1/3] 启动 Python 智能层..." -ForegroundColor Yellow
Start-Process powershell -ArgumentList "-NoExit", "-Command", "cd logic-python; uv run main.py"

# 3. 启动 Rust 加速层
Write-Host "[2/3] 启动 Rust 加速层..." -ForegroundColor Yellow
Start-Process powershell -ArgumentList "-NoExit", "-Command", "cd utils-rust; cargo run"

Write-Host "等待子服务就绪..." -ForegroundColor Gray
Start-Sleep -Seconds 5

# 4. 启动 Go 编排层 (使用标准 cmd 路径)
Write-Host "[3/3] 启动 Go 编排层..." -ForegroundColor Yellow
Start-Process powershell -ArgumentList "-NoExit", "-Command", "cd core-go; go run ./cmd/gateway"

Write-Host "`n系统启动指令已发出！" -ForegroundColor Magenta
Write-Host "网关入口地址: http://localhost:8080" -ForegroundColor White
