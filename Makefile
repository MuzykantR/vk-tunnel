.PHONY: build-server setup-bot systemd-install restart deploy logs-daemon logs-bot

# Путь к проекту на сервере
DIR = /opt/vk-vpn

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

deploy: build-server setup-bot restart
	@echo "Deploy successful."

logs-daemon:
	sudo journalctl -u vk-vpn-daemon -f

logs-bot:
	sudo journalctl -u vk-vpn-bot -f
