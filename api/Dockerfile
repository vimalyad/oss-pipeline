# The API image carries the dashboard too: the frontend is built here and
# served by Spring Boot from the same origin, so the browser never needs CORS
# and there is one port to protect.
#
# Build context is the repository root (see docker-compose.yml), because the
# frontend lives in web/ beside api/.

FROM docker.io/library/node:24-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM docker.io/library/maven:3-eclipse-temurin-21 AS api
WORKDIR /api
COPY api/pom.xml ./
RUN mvn -q -B dependency:go-offline
COPY api/src ./src
COPY --from=web /web/dist ./src/main/resources/static
RUN mvn -q -B -DskipTests package

FROM docker.io/library/eclipse-temurin:21-jre-alpine
RUN addgroup -S ossp && adduser -S -G ossp -u 1000 ossp
WORKDIR /app
COPY --from=api /api/target/api-0.1.0.jar app.jar
USER ossp
EXPOSE 8080
HEALTHCHECK --interval=15s --timeout=3s --retries=5 \
  CMD wget -qO- http://localhost:8080/actuator/health/readiness >/dev/null || exit 1
ENTRYPOINT ["java", "-XX:MaxRAMPercentage=75", "-jar", "app.jar"]
