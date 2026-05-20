import os
from dotenv import load_dotenv

load_dotenv()

BOT_TOKEN = os.getenv("BOT_TOKEN", "")
if not BOT_TOKEN:
    raise ValueError("BOT_TOKEN is not set in .env")

# Parse comma-separated whitelist IDs into a set of integers
whitelist_raw = os.getenv("WHITELIST_IDS", "")
WHITELIST_IDS = set()
for id_str in whitelist_raw.split(","):
    id_str = id_str.strip()
    if id_str.isdigit():
        WHITELIST_IDS.add(int(id_str))

MASTER_KEY_HEX = os.getenv("MASTER_KEY", "")
if not MASTER_KEY_HEX or len(MASTER_KEY_HEX) != 64:
    raise ValueError("MASTER_KEY must be exactly 64 hex characters (32 bytes)")
MASTER_KEY = bytes.fromhex(MASTER_KEY_HEX)

DAEMON_API_URL = os.getenv("DAEMON_API_URL", "http://127.0.0.1:8080/get_link")

# Доступ к api.telegram.org с VPS (часто блокируют). Примеры:
# TELEGRAM_PROXY=http://127.0.0.1:8118
# TELEGRAM_PROXY=socks5://127.0.0.1:1080
TELEGRAM_PROXY = os.getenv("TELEGRAM_PROXY", "").strip() or None

_tg_timeout = os.getenv("TELEGRAM_TIMEOUT_SEC", "120")
try:
    TELEGRAM_TIMEOUT_SEC = max(30, int(_tg_timeout))
except ValueError:
    TELEGRAM_TIMEOUT_SEC = 120
