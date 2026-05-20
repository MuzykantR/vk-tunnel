import aiohttp
from aiogram import Router, types
from aiogram.filters import Command
from config import WHITELIST_IDS, DAEMON_API_URL
from crypto import encrypt_link_payload

router = Router()

@router.message(Command(commands=["start", "get_link"]))
async def cmd_get_link(message: types.Message):
    # Whitelist check
    if message.from_user.id not in WHITELIST_IDS:
        return # Silent drop

    # Fetch link from Go daemon
    try:
        async with aiohttp.ClientSession() as session:
            async with session.get(DAEMON_API_URL, timeout=5) as resp:
                if resp.status != 200:
                    await message.answer("❌ Ошибка: Демон вернул не 200 статус.")
                    return
                data = await resp.json()
    except Exception as e:
        await message.answer(f"❌ Ошибка соединения с демоном: {e}")
        return

    link = data.get("link")
    server_pk = data.get("server_pk")

    if not link or not server_pk:
        await message.answer("❌ Ошибка: Неверный ответ от демона (отсутствует link или server_pk).")
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
