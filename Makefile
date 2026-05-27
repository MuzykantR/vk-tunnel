.PHONY: build-server setup-bot systemd-install restart restart-server restart-bot deploy deploy-server deploy-daemon deploy-bot logs-daemon logs-bot wrap wrap-show

# Путь к проекту на сервере
DIR = /opt/vk-vpn

# Drop-in для оверрайда env'ов сервиса без правки основного unit-файла.
# Удобно для toggle-флагов вроде VK_VPN_TURN_WRAP без `git diff`.
WRAP_DROPIN_DIR  = /etc/systemd/system/vk-vpn-daemon.service.d
WRAP_DROPIN_FILE = $(WRAP_DROPIN_DIR)/wrap.conf

build-server:
	@echo "Building Go daemon..."
	cd vk-vpn-server && go build -o vk-vpn-daemon main.go
	chmod +x vk-vpn-server/vk-vpn-daemon
	@echo "Build complete."

setup-bot:
	@echo "Setting up Python bot..."
	cd vk-vpn-bot && \
	if [ ! -d "venv" ]; then python3 -m venv venv; fi && \
	. venv/bin/activate && \
	pip install --upgrade pip && \
	pip install -r requirements.txt
	@echo "Bot setup complete."

systemd-install:
	@echo "Installing systemd services..."
	sudo cp systemd/vk-vpn-daemon.service /etc/systemd/system/
	sudo cp systemd/vk-vpn-bot.service /etc/systemd/system/
	sudo systemctl daemon-reload
	sudo systemctl enable vk-vpn-daemon
	sudo systemctl enable vk-vpn-bot
	@echo "Systemd services installed and enabled."

restart:
	@echo "Restarting services..."
	sudo systemctl restart vk-vpn-daemon vk-vpn-bot
	@echo "Services restarted."

restart-server:
	@echo "Restarting VK VPN Daemon..."
	sudo systemctl restart vk-vpn-daemon
	@echo "Daemon restarted."

restart-bot:
	@echo "Restarting Telegram Bot..."
	sudo systemctl restart vk-vpn-bot
	@echo "Bot restarted."

deploy: build-server setup-bot restart
	@echo "Deploy successful."

deploy-server: build-server restart-server
	@echo "Server deploy successful."

deploy-daemon: deploy-server

deploy-bot: setup-bot restart-bot
	@echo "Bot deploy successful."

logs-daemon:
	@# Файл усекается при каждом старте процесса (restart/deploy) — `-F`
	@# переоткрывает его при truncate, так что хвост не «застревает».
	sudo tail -F /var/log/vk-vpn-daemon.log

logs-daemon-journal:
	sudo journalctl -u vk-vpn-daemon -f

logs-bot:
	sudo journalctl -u vk-vpn-bot -f

# Включить / выключить protocol-aware WRAP обфускацию TURN ChannelData
# payload'а на сервере. Использует systemd drop-in, чтобы не править
# основной unit-файл и не сбивать git diff.
#
# Использование:
#   make wrap N=1     — включить (создаёт drop-in, daemon-reload, restart)
#   make wrap N=0     — выключить (удаляет drop-in, daemon-reload, restart)
#   make wrap-show    — показать текущее состояние и последние строки лога
#
# На клиенте (Windows) флаг ставится вручную перед запуском EXE:
#   PowerShell:  $env:VK_VPN_TURN_WRAP = "1"
#   cmd.exe:     set VK_VPN_TURN_WRAP=1
wrap:
	@if [ -z "$(N)" ]; then \
		echo "Usage: make wrap N=1 (enable) or make wrap N=0 (disable)"; \
		exit 1; \
	fi; \
	case "$(N)" in \
		1) \
			echo "Enabling VK_VPN_TURN_WRAP=1 via systemd drop-in..."; \
			sudo mkdir -p $(WRAP_DROPIN_DIR); \
			printf '[Service]\nEnvironment=VK_VPN_TURN_WRAP=1\n' \
				| sudo tee $(WRAP_DROPIN_FILE) > /dev/null; \
			sudo systemctl daemon-reload; \
			sudo systemctl restart vk-vpn-daemon; \
			echo "WRAP enabled. Verify with: make wrap-show"; \
			;; \
		0) \
			echo "Disabling VK_VPN_TURN_WRAP (removing drop-in)..."; \
			sudo rm -f $(WRAP_DROPIN_FILE); \
			sudo systemctl daemon-reload; \
			sudo systemctl restart vk-vpn-daemon; \
			echo "WRAP disabled. Verify with: make wrap-show"; \
			;; \
		*) \
			echo "N must be 0 or 1, got: $(N)"; exit 1; \
			;; \
	esac

wrap-show:
	@echo "=== Drop-in file ==="
	@if [ -f $(WRAP_DROPIN_FILE) ]; then \
		echo "Path: $(WRAP_DROPIN_FILE)"; \
		sudo cat $(WRAP_DROPIN_FILE); \
	else \
		echo "(no drop-in present — WRAP disabled by default)"; \
	fi
	@echo
	@echo "=== Resolved unit Environment ==="
	@sudo systemctl show vk-vpn-daemon -p Environment \
		| sed 's/ /\n/g' | grep -E 'VK_VPN_TURN_WRAP|^Environment=' || \
		echo "(VK_VPN_TURN_WRAP not present in resolved env)"
	@echo
	@echo "=== Recent wrap log lines ==="
	@sudo journalctl -u vk-vpn-daemon -n 200 --no-pager \
		| grep -iE 'turn-wrap|VK_VPN_TURN_WRAP' | tail -10 || \
		echo "(no wrap log lines in last 200 journal entries)"
