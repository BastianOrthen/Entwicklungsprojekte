# MHT (Multi-Hypothesis Tracker) — build from sibling SAW repo for proto
# Expected docker build context: ../  (i.e. Situation-Awareness/)
# Example:
#   docker build -f MHT/Dockerfile -t saw-mht:dev ..

FROM golang:1.24-alpine AS builder

WORKDIR /app

# Pull in SAW proto package (replace directive in MHT/go.mod points there)
COPY SituationAwarenessMap/go.mod SituationAwarenessMap/go.sum* ./SituationAwarenessMap/
COPY SituationAwarenessMap/proto/    ./SituationAwarenessMap/proto/
COPY SituationAwarenessMap/internal/  ./SituationAwarenessMap/internal/

# MHT module
COPY MHT/go.mod MHT/go.sum* ./MHT/
WORKDIR /app/MHT
RUN go mod download

COPY MHT/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /mht .

# Runtime
FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /mht .

EXPOSE 8090
ENTRYPOINT ["./mht"]
CMD ["-server","situation-awareness:50051","-mds-host","simulator","-mds-port","50511","-http",":8090"]
