ExUnit.configure(exclude: if(System.get_env("SANTATI_TEST_REDIS_URL"), do: [], else: [:redis]))

ExUnit.start()
