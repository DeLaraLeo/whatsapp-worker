# WhatsApp Worker

Worker em Go que integra WhatsApp com RabbitMQ para envio e recebimento de mensagens.

## 🚀 Instalação

### 1. Clone e configure
```bash
git clone <seu-repositorio>
cd whatsapp-worker
cp .env.example .env
```

### 2. Crie a rede Docker
```bash
docker network create bot-financeiro-network
```

### 3. Suba o bot-financeiro primeiro
```bash
# No diretório do bot-financeiro
docker compose up -d
```

### 4. Suba o WhatsApp Worker
```bash
# No diretório do whatsapp-worker
docker compose up -d
```

## 📱 Primeira Execução

Escaneie o QR Code que aparecerá nos logs:
```bash
docker compose logs -f whatsapp-worker
```

## 📨 Como Usar

### Enviar Mensagem
Publique na fila `q.message.send`:
```json
{
  "message_type": "text",
  "recipient_number": "5511999999999",
  "message_body": "Olá!",
  "transaction_id": "uuid-unique"
}
```

### Receber Mensagens
Escute a fila `q.message.receive`:
```json
{
  "message_type": "text",
  "sender_number": "5511888888888",
  "message_body": "Resposta",
  "message_id": "whatsapp_msg_id",
  "transaction_id": "uuid-unique"
}
```

## 🔧 Comandos Úteis

```bash
# Ver logs
docker compose logs whatsapp-worker

# Restart
docker compose restart whatsapp-worker

# Reautenticar WhatsApp
docker volume rm whatsapp-worker_whatsapp_storage
docker compose up -d
```

## 🔍 Monitoramento

- **RabbitMQ**: http://localhost:15672 (admin/admin123)
- **Filas**: `q.message.send` e `q.message.receive`
