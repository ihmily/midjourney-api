1. First, log in to your Discord account on the web, then click the 【Add a Server】 button in the left sidebar.

![exp1](./assets/example/en/example1.png)



2. Click to join a server.

![exp2](./assets/example/en/example2.png)



3. Enter the Midjourney invite link: http://discord.gg/midjourney to join the server.

![exp3](./assets/example/en/example3.png)



4. Click the Add a Server button again, this time select "Create My Own".

![exp4](./assets/example/en/example4.png)



5. Enter a name for the server you want to create.

![exp5](./assets/example/en/example5.png)



6. After creation, you can get the server ID and channel ID from the browser address bar, as shown below.

![exp6](./assets/example/en/example6.png)



7. Go to the official Midjourney server, and click on `Midjourney Bot` in the member list.

![exp10](./assets/example/en/example7.png)



8. Click the Add App button and select "Add to Server".

![exp11](./assets/example/en/example8.png)



9. Select the server you just created to add the Bot to it.

![exp12](./assets/example/en/example9.png)



10. Go to the midjourney-api server you created and test whether images can be generated normally.

![exp13](./assets/example/en/example10.png)



11. Press F12 to open the browser developer tools and obtain the User Token.

![exp14](./assets/example/en/example11.png)



At this point, the server ID, channel ID, and User Token required to run the program have all been obtained.

```
user_token: "User Token"
guild_id: "server ID"
channel_id: "channel ID"
```

Use these values when creating an account with `POST /api/v1/accounts`.
