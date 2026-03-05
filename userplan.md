## Processed the log as follow:

1.segment each connection attempt distinct log, and pcap files. Each connection attempt in pacp file will be identified by the local port as specified in the sdp, and named after the original file name concentrated with snowflake id.
2.inspect each connection attempt, and see what packet have received(not sent) and log message. And identify at which stage the connection attempt eventually reached. It can be unable to reach signaling server, did not receive any message from remote peer, did not received DTLS message from remote peer, did not finish dtls handshake with remote peer, handshake successful was the connection was too slow to finish tor bootstrap, or successful.
3.aggregate the numbers, and count the final stage of each connection to see what is the most likely reason a connection failed.
