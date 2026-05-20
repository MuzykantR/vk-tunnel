import asyncio
import logging
import socket
import sys

import aiohttp
from aiogram import Bot, Dispatcher
from aiogram.client.session.aiohttp import AiohttpSession
from aiogram.exceptions import TelegramNetworkError

from config import BOT_TOKEN, TELEGRAM_FORCE_IPV4, TELEGRAM_PROXY, TELEGRAM_TIMEOUT_SEC
from handlers import router

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

WEBHOOK_RETRIES = 3
WEBHOOK_RETRY_DELAY_SEC = 5


def make_bot_session() -> AiohttpSession:
    timeout = aiohttp.ClientTimeout(
        total=TELEGRAM_TIMEOUT_SEC,
        connect=min(60, TELEGRAM_TIMEOUT_SEC),
        sock_connect=min(60, TELEGRAM_TIMEOUT_SEC),
    )
    proxy = TELEGRAM_PROXY or None
    kwargs = {"timeout": timeout}
    # curl у вас ушёл в IPv6; aiohttp иногда висит на IPv4 — принудительно v4.
    if TELEGRAM_FORCE_IPV4 and not proxy:
        kwargs["connector"] = aiohttp.TCPConnector(family=socket.AF_INET)
        logger.info("Telegram HTTP client: IPv4 only")
    if proxy:
        logger.info("Telegram session via proxy %s", proxy.split("@")[-1])
    return AiohttpSession(proxy=proxy, **kwargs)


async def ensure_telegram_ready(bot: Bot) -> None:
    """getMe как ваш curl; delete_webhook опционален — polling часто и без него."""
    me = await bot.get_me(request_timeout=30)
    logger.info("Telegram API OK: @%s (id=%s)", me.username, me.id)

    for attempt in range(1, WEBHOOK_RETRIES + 1):
        try:
            await bot.delete_webhook(drop_pending_updates=True, request_timeout=30)
            logger.info("Webhook cleared")
            return
        except TelegramNetworkError as e:
            logger.warning("delete_webhook attempt %d/%d: %s", attempt, WEBHOOK_RETRIES, e)
            if attempt < WEBHOOK_RETRIES:
                await asyncio.sleep(WEBHOOK_RETRY_DELAY_SEC)

    logger.warning(
        "delete_webhook не ответил — всё равно запускаем polling "
        "(для long-polling webhook не обязателен)"
    )


async def main() -> None:
    session = make_bot_session()
    bot = Bot(token=BOT_TOKEN, session=session)
    dp = Dispatcher()
    dp.include_router(router)

    try:
        await ensure_telegram_ready(bot)
        logger.info("Starting bot polling (timeout=%ss)...", TELEGRAM_TIMEOUT_SEC)
        await dp.start_polling(bot, handle_signals=False)
    finally:
        await bot.session.close()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except TelegramNetworkError as e:
        logger.error(
            "Нет связи с api.telegram.org: %s\n"
            "Проверка: curl -4 -sS --max-time 15 "
            "'https://api.telegram.org/bot<TOKEN>/getMe'\n"
            "Если curl ок, а бот нет — git pull && make setup-bot && restart.",
            e,
        )
        sys.exit(1)
    except KeyboardInterrupt:
        logger.info("Bot stopped manually.")
