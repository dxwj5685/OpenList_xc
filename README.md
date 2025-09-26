添加星辰云盘存储（CZK）。docker命令：docker run -d \
  --name openlist-czk \
  --user 0:0 \
  -v /etc/openlist-czk:/opt/openlist/data \
  -p 5244:5244 \
  -e UMASK=022 \
  --restart unless-stopped \
  dxwj/openlist:latest


docker-compose：
services:
  openlist:
    image: 'dxwj/openlist:latest'
    container_name: openlist-czk
    user: '0:0' # Please replace `0:0` with the actual user ID and group ID you want to use to run OpenList.
    volumes:
      - '/etc/openlist-czk:/opt/openlist/data'
    ports:
      - '5244:5244'
    environment:
      - UMASK=022
    restart: unless-stopped
