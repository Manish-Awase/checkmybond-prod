# frontend/Dockerfile
# Dev-mode container — runs Vite dev server.
# For production, swap CMD for a build + nginx static serve stage.

FROM node:22-alpine
WORKDIR /app

COPY package.json ./
RUN npm install

COPY . .

EXPOSE 5173
CMD ["npm", "run", "dev", "--", "--host", "0.0.0.0"]
