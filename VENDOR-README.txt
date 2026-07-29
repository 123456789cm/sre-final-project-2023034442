本压缩包未预置 vendor 目录。首次放入联网的 sre-node1 后执行：

  cd ~/final-project
  bash scripts/generate-vendor.sh

执行成功后必须保留 go.sum 和 vendor/，再制作 SRE-00 快照或推送教师模板仓库。
