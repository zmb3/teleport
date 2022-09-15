---
authors: Marek Smoliński (marek@goteleport.com)
state: draft
---

# RFD 84 - Teleport Oracle Access Integration

## What


This RFD proposes a way to integrate Oracle Database Access with Teleport.
Oracle DB Access differs from currently supported database protocols by Teleport Database Access.
Namely, Oracle DB protocol is private and there is not any official documentation describing the Oracle protocol.

## Why

We want to increase the number of databases supported by Teleport and allow users to connect to Oracle using Teleport Database Access.
Where we want to provide a way for users to connect to Oracle databases through Teleport using the same Teleport UI/UX as for other supported databases.


# Scope of Integration
- **Teleport as Oracle Access Proxy**: Teleport Oracle DB agents should be able to act like a proxy between the incoming Oracle client connection and connection to Oracle Server where the Teleport DB Agent will terminate the incoming TLS connection and establish a new TLS connection to the Oracle Server using a new TLS Certificate and forward the traffic between Oracle client and server.
- **Audit Logging** (Optional): After TLS termination of incoming client Oracle connection Teleport DB Agents needs to be able to parse the Oracle wire protocol to provide Teleport audit logs and audit client interaction with Oracle database.


### TLS Termination of Incoming connection:
Teleport needs to be able TLS terminate incoming Oracle client connections to replace the TLS certificate with a new one signed by Teleport Database CA and establish a new TLS connection to the Oracle Server.
It seems that TLS connection between Oracle Client and Oracle Server can be renegotiated by some mechanism in L7 Oracle protocol. To support TLS termination on Teleport proxy side the TLS renegotiation triggered by Oracle Server needs to be also handled on Teleport DB Agent side.


## Details

### Teleport Database Access Configuration:

#### Oracle Client:

Oracle clients support TLS connections to the Oracle Server by using a custom container called [Oracle Wallet](https://docs.oracle.com/cd/E92519_02/pt856pbr3/eng/pt/tsvt/concept_UnderstandingOracleWallet.html#:~:text=Oracle%20Wallet%20is%20a%20container,is%20used%20for%20security%20credentials.
) that stores authentication credentials and certificates.
Teleport `tsh db login` command for Oracle database will generate cert in Oracle Wallet format allowing to configure the wallet in Oracle database clients like [sqlplus](https://docs.oracle.com/cd/B19306_01/server.102/b14357/qstart.htm) or [SQL Oracle Developer](https://www.oracle.com/database/sqldeveloper/)


##### UX:

Oracle will integrate with teleport in the same way as other databases.

* `tsh db connect` - would start [sqlplus](https://docs.oracle.com/cd/B19306_01/server.102/b14357/qstart.htm) Oracle CLI.
* `tsh proxy db` - would start proxy for 3rd party GUI clients like  [SQL Oracle Developer](https://www.oracle.com/database/sqldeveloper/)




#### Oracle Server Setup:
The new `tctl auth sign` `--format=oracle` sign format will be introduced where Teleport Database CA authority and generated certificate/key pair will be store in Oracle Wallet SSO autologin format:
```
tctl auth sign --format=oracle --host=oracle.example.com --out=server --ttl=2190h
```


Generated Oracle Wallet will be used in Oracle server [sqlnet.ora](https://docs.oracle.com/cd/E11882_01/network.112/e10835/sqlnet.htm#NETRF416) configuration file:
```
SSL_CLIENT_AUTHENTICATION = TRUE
SQLNET.AUTHENTICATION_SERVICES = (TCPS)
WALLET_LOCATION =
  (SOURCE =
    (METHOD = FILE)
    (METHOD_DATA =
      (DIRECTORY = /path/to/server/wallet)
    )
  )
```

and in [listener.ora](https://docs.oracle.com/database/121/NETRF/listener.htm#NETRF008) configuration file:
```
SSL_CLIENT_AUTHENTICATION = TRUE
WALLET_LOCATION =
  (SOURCE =
    (METHOD = FILE)
    (METHOD_DATA =
      (DIRECTORY =  /path/to/server/wallet)
    )
  )

LISTENER =
   (DESCRIPTION_LIST =
     (DESCRIPTION =
       (ADDRESS = (PROTOCOL = TCPS)(HOST = 0.0.0.0)(PORT = 2484))
     )
   )
```

Additionally, the following server parameters to will be set to enable TLS authentication on the server side:
\
[SQLNET.AUTHENTICATION_SERVICES](https://docs.oracle.com/cd/E11882_01/network.112/e10835/sqlnet.htm#NETRF2035)
\
[SSL_CLIENT_AUTHENTICATION](https://docs.oracle.com/cd/E11882_01/network.112/e10835/sqlnet.htm#NETRF233)

#### Create a OracleDB User wth TLS x509 DN Authentication:
Oracle server allows to authenticate database user based on the certificate CN field:

```azure
CREATE USER alice IDENTIFIED EXTERNALLY AS 'CN=alice;
```
Ref: [Configuring Authentication Using PKI Certificates for Centrally Managed Users](https://docs.oracle.com/en/database/oracle/oracle-database/19/dbseg/integrating_mads_with_oracle_database.html#GUID-1EF17156-3FA4-4EDD-8DFF-F98EB3A926BF)

## Security
Teleport Oracle Database access will not differ from other supported database protocols in terms of security.
The connection between Teleport Database Agent and Oracle Server will be secured by TLS 1.2 and mutual TLS authentication.
