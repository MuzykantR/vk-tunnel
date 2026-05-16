import json
import time
import os
import base64
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from config import MASTER_KEY

def encrypt_link_payload(link: str, server_pk: str) -> str:
    """
    Encrypts the link and server_pk into a base64url encoded myvpn:// URI.
    Format: myvpn://v1:<IV_BASE64URL>:<CIPHERTEXT_WITH_TAG_BASE64URL>
    """
    payload = {
        "link": link,
        "ts": int(time.time()),
        "server_pk": server_pk
    }
    
    payload_bytes = json.dumps(payload).encode('utf-8')
    
    # Generate 12-byte IV for GCM
    iv = os.urandom(12)
    
    encryptor = Cipher(
        algorithms.AES(MASTER_KEY),
        modes.GCM(iv),
    ).encryptor()
    
    ciphertext = encryptor.update(payload_bytes) + encryptor.finalize()
    auth_tag = encryptor.tag # 16 bytes
    
    # Standard GCM layout: ciphertext + auth_tag
    ciphertext_with_tag = ciphertext + auth_tag
    
    # Encode to base64url without padding
    iv_b64 = base64.urlsafe_b64encode(iv).decode('ascii').rstrip('=')
    cipher_b64 = base64.urlsafe_b64encode(ciphertext_with_tag).decode('ascii').rstrip('=')
    
    uri = f"myvpn://v1:{iv_b64}:{cipher_b64}"
    return uri
