Everyday Carry
==============

Server that provides a live poll environment where the same questions are presented and
everyone enters their answers.  Answer updates are pushed live to all connected.

Current
-------
- Questions entered manually in redis
- Answers may be chosen or a new answer can be entered
- Answers are saved for users via a persistent cookie session


TODO
----
- Allow entry fo new questions
- Create the concept of "rounds"
	- Only a single question is "current"
	- Current question stays active for a certain period of time
	- Once a user enters the answer for a question, she may enter a new question or vote for an existing question to go next

- Code cleanup
	- Refactor to be more MVC and separate concerns
	- Separate js file
	- Create listener groups so that full broadcasts aren't always required

- Prettify
	- Get someone who knows how to do this to do it
