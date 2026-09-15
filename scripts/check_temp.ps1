# 温度诊断脚本
# 用法：在项目根目录运行 .\scripts\check_temp.ps1

Write-Host "=== 温度传感器诊断 ===" -ForegroundColor Cyan

# 1. 测试 WMI 热区传感器
Write-Host "`n[1] 测试 WMI 热区传感器..."
try {
    $thermal = Get-CimInstance -Namespace root/wmi -ClassName MSAcpi_ThermalZoneTemperature -ErrorAction Stop
    if ($thermal) {
        foreach ($t in $thermal) {
            $temp = [math]::Round(($t.CurrentTemperature / 10) - 273.15, 1)
            Write-Host "  找到热区: $($t.InstanceName) -> ${temp}°C" -ForegroundColor Green
        }
    } else {
        Write-Host "  未找到任何热区传感器" -ForegroundColor Yellow
    }
} catch {
    Write-Host "  WMI 访问失败: $($_.Exception.Message)" -ForegroundColor Red
}

# 2. 测试 nvidia-smi
Write-Host "`n[2] 测试 NVIDIA GPU 温度..."
if (Get-Command nvidia-smi -ErrorAction SilentlyContinue) {
    $gpuTemp = nvidia-smi --query-gpu=temperature.gpu --format=csv,noheader,nounits
    Write-Host "  GPU 温度: ${gpuTemp}°C" -ForegroundColor Green
} else {
    Write-Host "  nvidia-smi 不可用（可能没有 NVIDIA 显卡）" -ForegroundColor Yellow
}

# 3. 测试 SMART 磁盘温度
Write-Host "`n[3] 测试 SMART 磁盘温度..."
try {
    $smart = Get-CimInstance -Namespace root/wmi -ClassName MSStorageDriver_ATAPISmartData -ErrorAction Stop
    if ($smart) {
        Write-Host "  找到 $($smart.Count) 个磁盘的 SMART 数据" -ForegroundColor Green
        # 注意：SMART 原始数据解析较复杂，这里只确认能访问
    } else {
        Write-Host "  未找到 SMART 数据" -ForegroundColor Yellow
    }
} catch {
    Write-Host "  SMART 访问失败: $($_.Exception.Message)" -ForegroundColor Red
}

Write-Host "`n=== 诊断完成 ===" -ForegroundColor Cyan
Write-Host "如果 WMI 热区传感器可用，监控程序应该能显示 CPU 温度。"
Write-Host "如果显示 '拒绝访问'，请以管理员身份运行此脚本重试。"
