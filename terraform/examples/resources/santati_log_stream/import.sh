# By the stream's integer id. The credential in auth is never returned by the
# API, so add the auth block to the configuration and apply to set it.
terraform import santati_log_stream.siem 12
