// 生成自签名 CA 证书，输出到 certs/ca.crt 和 certs/ca.key
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log"
	"math/big"
	"os"
	"time"
)

func main() {
	os.MkdirAll("certs", 0755)

	// 生成 EC 私钥
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}

	// CA 证书模板
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName:   "Kiro Proxy CA",
			Organization: []string{"Kiro Proxy"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour), // 10年
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	// 自签名
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}

	// 写 certs/ca.crt
	certFile, err := os.Create("certs/ca.crt")
	if err != nil {
		log.Fatal(err)
	}
	pem.Encode(certFile, &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	certFile.Close()

	// 写 certs/ca.key
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		log.Fatal(err)
	}
	keyFile, err := os.Create("certs/ca.key")
	if err != nil {
		log.Fatal(err)
	}
	pem.Encode(keyFile, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	keyFile.Close()

	log.Println("✓ 生成完成:")
	log.Println("  certs/ca.crt  (安装到系统信任链)")
	log.Println("  certs/ca.key  (代理使用，勿泄露)")
}
