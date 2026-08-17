# 前端镜像：构建后由 nginx 提供静态文件
FROM node:22-alpine AS build
WORKDIR /app
COPY package.json package-lock.json* /app/
RUN npm install
COPY . /app
RUN npm run build

FROM nginx:1.27-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 80
