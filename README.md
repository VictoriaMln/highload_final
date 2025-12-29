# Go Highload Service

Высоконагруженный HTTP-сервис с поддержкой rolling-анализа, мониторинга и горизонтального масштабирования в Kubernetes.

Проект демонстрирует:
- stateless-архитектуру,
- хранение состояния во внешнем хранилище (Redis),
- экспорт Prometheus-метрик,
- автоматическое масштабирование с помощью Horizontal Pod Autoscaler (HPA).


## Функциональность

- Прием метрик нагрузки (CPU, RPS)
- Rolling-анализ входящих данных (скользящее окно)
- Вычисление:
  - среднего значения (rolling average)
  - стандартного отклонения
  - z-score
  - флага аномалии
- Экспорт метрик в формате Prometheus
- Горизонтальное масштабирование в Kubernetes


## HTTP API

### POST `/ingest`
Прим метрик нагрузки.

**Пример запроса:**
```
{
  "cpu": 12,
  "rps": 120
}
```
### GET `/analyze`
Возвращает текущее состояние rolling-анализа.

**Пример ответа:**

```
{
  "count": 9,
  "windowSize": 50,
  "rollingAvg": 120.3,
  "stdDev": 1.76,
  "zScore": -0.18,
  "isAnomaly": false,
  "lastRps": 120,
  "lastCpu": 12,
  "computedAt": 1766925730
}
```
### GET `/metrics`
Экспорт метрик в формате Prometheus.

**Примеры метрик:**

 - ingest_requests_total

 - anomalies_total

 - runtime-метрики Go

## Архитектура
Система состоит из следующих компонентов:

 - Go-сервис — обработка HTTP-запросов и расчет метрик

 - Redis — хранение состояния rolling window

 - Kubernetes — деплой и масштабирование

 - Prometheus-совместимые метрики — мониторинг

 - Horizontal Pod Autoscaler (HPA) — autoscaling по CPU

Сервис является stateless, все состояние вынесено во внешнее хранилище (Redis).

## Сборка Docker-образа
```
docker build -t go-highload-service .
```
## Развертывание в Kubernetes
1. Redis разворачивается с использованием Helm-чарта:

```
helm repo add bitnami https://charts.bitnami.com/bitnami
helm install redis bitnami/redis
```
2. Go-сервис
```
kubectl apply -f go-deployment.yaml
kubectl apply -f go-service.yaml
```
Проверка:

```
kubectl get pods
kubectl get svc
```
## Autoscaling (HPA)
Horizontal Pod Autoscaler настроен со следующими параметрами:

 - minReplicas: 2

 - maxReplicas: 5

 - масштабирование по CPU

 - целевая загрузка CPU: 70%

```
kubectl apply -f go-hpa.yaml
kubectl get hpa
```
При превышении порога CPU количество реплик увеличивается автоматически.

## Генерация нагрузки
Для проверки autoscaling нагрузка генерируется внутри кластера:

```
kubectl run loadgen --rm -it --image=busybox:1.36 --restart=Never -- sh
```
```
while true; do
  wget -qO- \
    --post-data='{"cpu":12,"rps":120}' \
    --header='Content-Type: application/json' \
    http://go-highload:8080/ingest > /dev/null
done
```
## Мониторинг
Метрики доступны по endpoint /metrics и могут быть использованы Prometheus или HPA для масштабирования.

## Требования
 - Go 1.22+

 - Docker

 - Kubernetes (Minikube)

 - Helm
