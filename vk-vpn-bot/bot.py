import asyncio
import logging
import sys

import aiohttp
from aiogram import Bot, Dispatcher
from aiogram.client.session.aiohttp import AiohttpSession
from aiogram.exceptions import TelegramNetworkError

from config import BOT_TOKEN, TELEGRAM_PROXY, TELEGRAM_TIMEOUT_SEC
from handlers import router

logging.basicConfig(level=logging.INFO)
logger = logging.getLogger(__name__)

STARTUP_RETRIES = 30
STARTUP_RETRY_DELAY_SEC = 10


def make_bot_session() -> AiohttpSession:
    timeout = aiohttp.ClientTimeout(
        total=TELEGRAM_TIMEOUT_SEC,
        connect=min(60, TELEGRAM_TIMEOUT_SEC),
        sock_connect=min(60, TELEGRAM_TIMEOUT_SEC),
    )
    proxy = TELEGRAM_PROXY or None
    if proxy:
        logger.info("Telegram session via proxy %s", proxy.split("@")[-1])
    return AiohttpSession(proxy=proxy, timeout=timeout)


async def ensure_webhook_cleared(bot: Bot) -> None:
    for attempt in range(1, STARTUP_RETRIES + 1):
        try:
            await bot.delete_webhook(drop_pending_updates=True)
            logger.info("Webhook cleared")
            return
        except TelegramNetworkError as e:
            logger.warning(
                "delete_webhook attempt %d/%d failed: %s",
                attempt,
                STARTUP_RETRIES,
                e,
            )
            if attempt >= STARTUP_RETRIES:
                raise
            await asyncio.sleep(STARTUP_RETRY_DELAY_SEC)


async def main() -> None:
    session = make_bot_session()
    bot = Bot(token=BOT_TOKEN, session=session)
    dp = Dispatcher()
    dp.include_router(router)

    try:
        await ensure_webhook_cleared(bot)
        logger.info("Starting bot polling (timeout=%ss)...", TELEGRAM_TIMEOUT_SEC)
        await dp.start_polling(bot)
    finally:
        await bot.session.close()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except TelegramNetworkError as e:
        logger.error(
            "Нет связи с api.telegram.org: %s\n"
            "На VPS: curl -sS --max-time 15 https://api.telegram.org/bot<TOKEN>/getMe\n"
            "Если таймаут — добавьте в .env: TELEGRAM_PROXY=socks5://127.0.0.1:1080 "
            "(нужен pip install aiohttp-socks)",
            e,
        )
        sys.exit(1)
    except KeyboardInterrupt:
        logger.info("Bot stopped manually.")
