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


@router.message(Command("id"))
async def cmd_id(message: types.Message):
    """Показывает Telegram user_id для WHITELIST_IDS (без whitelist)."""
    uid = message.from_user.id
    allowed = uid in WHITELIST_IDS
    await message.answer(
        f"Ваш Telegram user_id: {uid}\n"
        f"В whitelist: {'да' if allowed else 'нет'}\n"
        f"Записей в whitelist: {len(WHITELIST_IDS)}"
    )


@router.message(Command(commands=["start", "get_link"]))
async def cmd_get_link(message: types.Message):
    uid = message.from_user.id
    if uid not in WHITELIST_IDS:
        logger.warning("ignored /get_link from user_id=%s (not in whitelist)", uid)
        await message.answer(
            f"⛔ Доступ запрещён (user_id {uid} не в WHITELIST_IDS).\n"
            f"Отправьте /id — скопируйте число в .env бота."
        )
        return

    link, server_pk, err = await _fetch_link_from_daemon()
    if err:
        await message.answer(f"❌ {err}")
        return

    try:
        uri = encrypt_link_payload(link, server_pk)
    except Exception as e:
        logger.exception("encrypt failed")
        await message.answer(f"❌ Ошибка шифрования: {e}")
        return

    text = (
        "🟢 VPN-туннель готов. Скопируйте ссылку в vk-client:\n\n"
        f"{uri}"
    )
    try:
        # Без Markdown: в myvpn:// много '_' — Telegram ломает разбор entities.
        await message.answer(text)
    except Exception as e:
        logger.exception("telegram send failed")
        await message.answer(f"❌ Не удалось отправить сообщение в Telegram: {e}")
