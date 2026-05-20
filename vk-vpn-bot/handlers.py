import asyncio
import logging
import time

import aiohttp
from aiogram import Router, types
from aiogram.filters import Command
from config import WHITELIST_IDS, DAEMON_API_URL
from crypto import encrypt_link_payload

router = Router()
logger = logging.getLogger(__name__)

# Daemon sets currentLink only after the first successful calls.start; until then /get_link returns "".
LINK_WAIT_SEC = 45
LINK_POLL_INTERVAL_SEC = 1.5


async def _fetch_link_from_daemon():
    """Returns (link, server_pk, error_message). error_message is set on hard failure."""
    deadline = time.monotonic() + LINK_WAIT_SEC
    last_empty = False
    async with aiohttp.ClientSession() as session:
        while time.monotonic() < deadline:
            try:
                async with session.get(DAEMON_API_URL, timeout=5) as resp:
                    if resp.status != 200:
                        return None, None, f"демон вернул HTTP {resp.status}"
                    data = await resp.json()
            except Exception as e:
                return None, None, f"нет связи с демоном ({DAEMON_API_URL}): {e}"

            link = (data.get("link") or "").strip()
            server_pk = (data.get("server_pk") or "").strip()
            if link and server_pk:
                return link, server_pk, None
            last_empty = True
            await asyncio.sleep(LINK_POLL_INTERVAL_SEC)

    if last_empty:
        return (
            None,
            None,
            "демон запущен, но звонок ещё не создан (пустой link). "
            "Проверьте лог vk-vpn-daemon: `Failed to fetch call URL` или cookies.json.",
        )
    return None, None, "таймаут ожидания ссылки от демона"


@router.message(Command(commands=["start", "get_link"]))
async def cmd_get_link(message: types.Message):
    # Whitelist check
    if message.from_user.id not in WHITELIST_IDS:
        logger.info("ignored /get_link from non-whitelist user_id=%s", message.from_user.id)
        return  # Silent drop (looks like «бот молчит»)

    link, server_pk, err = await _fetch_link_from_daemon()
    if err:
        await message.answer(f"❌ {err}")
        return

    # Encrypt
    try:
        uri = encrypt_link_payload(link, server_pk)
    except Exception as e:
        await message.answer(f"❌ Ошибка шифрования: {e}")
        return

    # Send success response
    text = (
        "🟢 Ваш защищенный VPN-туннель готов.\n"
        "Нажмите на ссылку ниже, чтобы подключиться:\n\n"
        f"`{uri}`"
    )
    
    await message.answer(text, parse_mode="Markdown")
