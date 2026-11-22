# nginx/Dockerfile
FROM nginx:1.25-alpine

# Remove default config
RUN rm /etc/nginx/conf.d/default.conf

# Copy our config
COPY nginx.conf /etc/nginx/nginx.conf

# nginx.conf points access_log/error_log at real files under /var/log/nginx —
# without this, those logs are invisible to `docker compose logs nginx` and
# vanish when the container is removed (nothing mounts that path). Symlinking
# to stdout/stderr restores the behavior the base nginx image normally has
# by default, so container logs actually show request traffic again.
RUN ln -sf /dev/stdout /var/log/nginx/access.log && \
    ln -sf /dev/stderr /var/log/nginx/error.log

# SSL certs dir — mounted via docker-compose volume
RUN mkdir -p /etc/nginx/ssl /var/www/certbot

EXPOSE 80 443

CMD ["nginx", "-g", "daemon off;"]
